// Package backup implements the IMAP backup pipeline.
package backup

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/emersion/go-message"

	"github.com/hjiang/mnemosyne/internal/accounts"
	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/extract"
	"github.com/hjiang/mnemosyne/internal/messages"
)

// Result summarizes a backup run.
type Result struct {
	NewMessages  int
	NewLocations int
	Errors       []error
}

// Orchestrator drives the backup pipeline for an IMAP account.
type Orchestrator struct {
	accounts *accounts.Repo
	messages *messages.Repo
	blobs    *blobs.Store
}

// NewOrchestrator creates a backup orchestrator.
func NewOrchestrator(accts *accounts.Repo, msgs *messages.Repo, store *blobs.Store) *Orchestrator {
	return &Orchestrator{
		accounts: accts,
		messages: msgs,
		blobs:    store,
	}
}

// Run backs up all enabled folders for the given account.
func (o *Orchestrator) Run(accountID, userID int64) (*Result, error) {
	acct, err := o.accounts.GetByID(accountID, userID)
	if err != nil {
		return nil, fmt.Errorf("loading account: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", acct.Host, acct.Port)
	client, err := imapwrap.Dial(addr, acct.Username, acct.Password, acct.UseTLS)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer client.Close() //nolint:errcheck

	folders, err := o.accounts.ListFolders(accountID)
	if err != nil {
		return nil, fmt.Errorf("listing folders: %w", err)
	}

	result := &Result{}
	for _, folder := range folders {
		if !folder.Enabled {
			continue
		}
		if err := o.syncFolder(client, folder, userID, result); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("folder %q: %w", folder.Name, err))
		}
	}

	now := time.Now().Unix()
	_ = o.accounts.SetLastSyncAt(accountID, now)
	return result, nil
}

func (o *Orchestrator) syncFolder(
	client *imapwrap.Client,
	folder *accounts.Folder,
	userID int64,
	result *Result,
) error {
	info, err := client.SelectFolder(folder.Name)
	if err != nil {
		return fmt.Errorf("selecting: %w", err)
	}

	// UIDVALIDITY check.
	if folder.UIDValidity != nil && *folder.UIDValidity != info.UIDValidity {
		if err := o.messages.DeleteLocationsByFolder(folder.ID); err != nil {
			return fmt.Errorf("clearing locations: %w", err)
		}
		if err := o.accounts.SetLastSeenUID(folder.ID, 0); err != nil {
			return fmt.Errorf("resetting last_seen_uid: %w", err)
		}
		folder.LastSeenUID = 0
	}

	if err := o.accounts.SetUIDValidity(folder.ID, info.UIDValidity); err != nil {
		return fmt.Errorf("storing uidvalidity: %w", err)
	}

	if info.NumMessages == 0 {
		return nil
	}

	startUID := folder.LastSeenUID + 1
	envs, err := client.FetchEnvelopes(startUID, 0)
	if err != nil {
		return fmt.Errorf("fetching envelopes: %w", err)
	}

	if len(envs) == 0 {
		return nil
	}

	var maxUID uint32
	for _, env := range envs {
		if env.UID > maxUID {
			maxUID = env.UID
		}

		body, fetchErr := client.FetchBody(env.UID)
		if fetchErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("UID %d: %w", env.UID, fetchErr))
			continue
		}

		newMsg, storeErr := o.storeMessage(body, env, folder.ID, userID)
		if storeErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("UID %d store: %w", env.UID, storeErr))
			continue
		}

		if newMsg {
			result.NewMessages++
		}
		result.NewLocations++
	}

	if maxUID > 0 {
		_ = o.accounts.SetLastSeenUID(folder.ID, maxUID)
	}
	return nil
}

func (o *Orchestrator) storeMessage(
	body []byte,
	env imapwrap.Envelope,
	folderID, userID int64,
) (bool, error) {
	hash := sha256.Sum256(body)

	exists, err := o.messages.ExistsByHash(hash[:], userID)
	if err != nil {
		return false, fmt.Errorf("checking existence: %w", err)
	}

	isNew := !exists
	if isNew {
		// Crash-safe ordering: blob → message row → location row.
		if _, err := o.blobs.Put(bytes.NewReader(body)); err != nil {
			return false, fmt.Errorf("writing blob: %w", err)
		}

		hasAtt := hasAttachments(body)
		bodyText := ExtractBodyText(body)
		msg := &messages.Message{
			Hash:           hash[:],
			UserID:         userID,
			MessageID:      env.MessageID,
			FromAddr:       env.From,
			ToAddrs:        env.To,
			CcAddrs:        env.Cc,
			Subject:        env.Subject,
			Date:           &env.Date,
			Size:           int64(len(body)),
			HasAttachments: hasAtt,
			BodyText:       bodyText,
		}
		if err := o.messages.Insert(msg); err != nil {
			return false, fmt.Errorf("inserting message: %w", err)
		}

		// Index into FTS5.
		if rowid, err := o.messages.GetRowID(hash[:]); err == nil {
			_ = o.messages.IndexFTS(rowid, env.Subject, env.From, env.To, env.Cc, bodyText)
		}

		if hasAtt {
			o.storeAttachments(body, hash[:])
		}
	}

	loc := &messages.Location{
		MessageHash: hash[:],
		FolderID:    folderID,
		UID:         env.UID,
	}
	if env.Date != 0 {
		loc.InternalDate = &env.Date
	}
	if err := o.messages.InsertLocation(loc); err != nil {
		return false, fmt.Errorf("inserting location: %w", err)
	}

	return isNew, nil
}

func (o *Orchestrator) storeAttachments(body, msgHash []byte) {
	entity, err := message.Read(bytes.NewReader(body))
	if err != nil {
		return
	}

	mr := entity.MultipartReader()
	if mr == nil {
		return
	}

	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}

		mediaType, params, _ := part.Header.ContentType()
		disp, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

		filename := dispParams["filename"]
		if filename == "" {
			filename = params["name"]
		}

		if disp != "attachment" && filename == "" {
			continue
		}

		partBody, readErr := io.ReadAll(part.Body)
		if readErr != nil {
			continue
		}

		partHash, putErr := o.blobs.Put(bytes.NewReader(partBody))
		if putErr != nil {
			continue
		}

		_ = o.messages.InsertAttachment(&messages.Attachment{
			MessageHash:   msgHash,
			Filename:      filename,
			MimeType:      mediaType,
			Size:          int64(len(partBody)),
			BlobHash:      partHash,
			TextExtracted: 0,
		})
	}
}

// ExtractBodyText parses a raw MIME message and returns the text body.
// For simple text/plain messages, it returns the body directly.
// For multipart messages, it prefers text/plain over text/html.
func ExtractBodyText(raw []byte) string {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return ""
	}

	mediaType, _, _ := entity.Header.ContentType()

	if strings.HasPrefix(mediaType, "text/plain") {
		text, err := io.ReadAll(entity.Body)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(text))
	}

	if strings.HasPrefix(mediaType, "text/html") {
		ext := &extract.HTMLExtractor{}
		text, err := ext.Extract(entity.Body)
		if err != nil {
			return ""
		}
		return text
	}

	mr := entity.MultipartReader()
	if mr == nil {
		return ""
	}

	var htmlFallback string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}

		partType, _, _ := part.Header.ContentType()

		if partType == "text/plain" {
			text, err := io.ReadAll(part.Body)
			if err != nil {
				continue
			}
			return strings.TrimSpace(string(text))
		}

		if partType == "text/html" && htmlFallback == "" {
			ext := &extract.HTMLExtractor{}
			text, err := ext.Extract(part.Body)
			if err != nil {
				continue
			}
			htmlFallback = text
		}
	}

	return htmlFallback
}

func hasAttachments(body []byte) bool {
	limit := len(body)
	if limit > 2048 {
		limit = 2048
	}
	lower := strings.ToLower(string(body[:limit]))
	return strings.Contains(lower, "multipart/mixed") ||
		strings.Contains(lower, "content-disposition: attachment")
}
