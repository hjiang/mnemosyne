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
	_ "github.com/emersion/go-message/charset" // registers charset decoders for RFC 2047

	"github.com/hjiang/mnemosyne/internal/accounts"
	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/backup/policy"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/extract"
	"github.com/hjiang/mnemosyne/internal/messages"
)

// Progress reports the current state of a backup run.
type Progress struct {
	Folder       string `json:"folder"`
	FolderIndex  int    `json:"folder_index"`
	FolderTotal  int    `json:"folder_total"`
	NewMessages  int    `json:"new_messages"`
	NewLocations int    `json:"new_locations"`
}

// ProgressFunc is called after each folder sync to report progress.
type ProgressFunc func(p Progress)

// Result summarizes a backup run.
type Result struct {
	NewMessages  int
	NewLocations int
	Errors       []error
}

// IMAPClient abstracts IMAP operations for the backup pipeline.
type IMAPClient interface {
	SelectFolder(name string) (*imapwrap.FolderInfo, error)
	FetchEnvelopes(startUID, endUID uint32) ([]imapwrap.Envelope, error)
	FetchBody(uid uint32) ([]byte, error)
	MarkDeleted(uids []uint32) error
	Expunge() error
	Close() error
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
// If onProgress is non-nil, it is called after each folder sync.
func (o *Orchestrator) Run(accountID, userID int64, onProgress ProgressFunc) (*Result, error) {
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

	var enabled []*accounts.Folder
	for _, f := range folders {
		if f.Enabled {
			enabled = append(enabled, f)
		}
	}

	result := &Result{}
	for i, folder := range enabled {
		if err := o.syncFolder(client, folder, userID, result); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("folder %q: %w", folder.Name, err))
		}
		if onProgress != nil {
			onProgress(Progress{
				Folder:       folder.Name,
				FolderIndex:  i + 1,
				FolderTotal:  len(enabled),
				NewMessages:  result.NewMessages,
				NewLocations: result.NewLocations,
			})
		}
	}

	now := time.Now().Unix()
	_ = o.accounts.SetLastSyncAt(accountID, now)
	return result, nil
}

func (o *Orchestrator) syncFolder(
	client IMAPClient,
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
	var hadError bool
	for _, env := range envs {
		// Skip messages already stored from a previous (partial) run.
		if exists, _ := o.messages.LocationExistsByFolderAndUID(folder.ID, env.UID); exists {
			if !hadError && env.UID > maxUID {
				maxUID = env.UID
			}
			continue
		}

		body, fetchErr := client.FetchBody(env.UID)
		if fetchErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("UID %d: %w", env.UID, fetchErr))
			hadError = true
			continue
		}

		newMsg, storeErr := o.storeMessage(body, env, folder.ID, userID)
		if storeErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("UID %d store: %w", env.UID, storeErr))
			hadError = true
			continue
		}

		if !hadError && env.UID > maxUID {
			maxUID = env.UID
		}

		if newMsg {
			result.NewMessages++
		}
		result.NewLocations++
	}

	if maxUID > 0 {
		_ = o.accounts.SetLastSeenUID(folder.ID, maxUID)
	}

	// Apply retention policy: delete old messages from the IMAP server
	// now that we've confirmed they're backed up locally.
	if err := o.applyRetention(client, folder); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("folder %q retention: %w", folder.Name, err))
	}

	return nil
}

func (o *Orchestrator) applyRetention(client IMAPClient, folder *accounts.Folder) error {
	locs, err := o.messages.ListLocationsByFolder(folder.ID)
	if err != nil {
		return fmt.Errorf("listing locations: %w", err)
	}

	msgs := make([]policy.Message, len(locs))
	for i, loc := range locs {
		msgs[i] = policy.Message{UID: loc.UID}
		if loc.InternalDate != nil {
			msgs[i].InternalDate = *loc.InternalDate
		}
	}

	return ApplyRetention(client, folder.PolicyJSON, msgs, true, time.Now())
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

// ExtractSubject parses the Subject header from a raw RFC 822 message,
// decoding any RFC 2047 encoded-words. Returns empty string on failure.
func ExtractSubject(raw []byte) string {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	subject, err := entity.Header.Text("Subject")
	if err != nil {
		return ""
	}
	return subject
}

// ExtractBodyText parses a raw MIME message and returns the text body.
// For simple text/plain messages, it returns the body directly.
// For multipart messages, it recurses into nested parts and prefers
// text/plain over text/html.
func ExtractBodyText(raw []byte) string {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return extractEntityText(entity)
}

// extractEntityText extracts body text from a single MIME entity,
// recursing into multipart containers.
func extractEntityText(entity *message.Entity) string {
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

		if strings.HasPrefix(partType, "multipart/") {
			if text := extractEntityText(part); text != "" {
				return text
			}
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
