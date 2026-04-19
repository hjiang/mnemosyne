// Package backup implements the IMAP backup pipeline.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"sort"
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
	ErrorCount   int    `json:"error_count,omitempty"`
	Done         bool   `json:"done,omitempty"`
}

// ProgressFunc is called before each folder sync to report progress.
type ProgressFunc func(p Progress)

// Result summarizes a backup run.
type Result struct {
	NewMessages  int
	NewLocations int
	NewEnvelopes int // envelope fetches count as progress for retry decisions
	Errors       []error
}

// IMAPClient abstracts IMAP operations for the backup pipeline.
type IMAPClient interface {
	SelectFolder(name string) (*imapwrap.FolderInfo, error)
	FetchEnvelopes(startUID, endUID uint32) ([]imapwrap.Envelope, error)
	FetchBody(uid uint32) ([]byte, error)
	FetchBodies(uids []uint32) (map[uint32][]byte, []uint32, error)
	MarkDeleted(uids []uint32) error
	Expunge() error
	Close() error
}

// fetchBatchSize is the number of message bodies fetched per IMAP FETCH command.
const fetchBatchSize = 50

// connError signals that syncFolder stopped due to a connection-level failure.
// Run uses this to decide whether reconnecting and retrying is worthwhile.
type connError struct{ err error }

func (e *connError) Error() string { return e.err.Error() }
func (e *connError) Unwrap() error { return e.err }

// TokenRefresher obtains a fresh OAuth2 access token for an account.
type TokenRefresher interface {
	EnsureFreshToken(ctx context.Context, accountID, userID int64) (accessToken string, err error)
}

// Orchestrator drives the backup pipeline for an IMAP account.
type Orchestrator struct {
	accounts      *accounts.Repo
	messages      *messages.Repo
	blobs         *blobs.Store
	tokenRefresh  TokenRefresher
	dialFunc      func(addr, user, pass string, tls bool, proxyConf *imapwrap.ProxyConfig) (IMAPClient, error) // nil = use imapwrap.Dial
	dialOAuthFunc func(addr, user, token string, tls bool) (IMAPClient, error)                                 // nil = use imapwrap.DialOAuth
}

// NewOrchestrator creates a backup orchestrator.
func NewOrchestrator(accts *accounts.Repo, msgs *messages.Repo, store *blobs.Store, tokenRefresh TokenRefresher) *Orchestrator {
	return &Orchestrator{
		accounts:     accts,
		messages:     msgs,
		blobs:        store,
		tokenRefresh: tokenRefresh,
	}
}

func (o *Orchestrator) dial(addr, user, pass string, tls bool, proxyConf *imapwrap.ProxyConfig) (IMAPClient, error) {
	if o.dialFunc != nil {
		return o.dialFunc(addr, user, pass, tls, proxyConf)
	}
	return imapwrap.Dial(addr, user, pass, tls, proxyConf)
}

func (o *Orchestrator) dialOAuth(addr, user, token string, tls bool) (IMAPClient, error) {
	if o.dialOAuthFunc != nil {
		return o.dialOAuthFunc(addr, user, token, tls)
	}
	return imapwrap.DialOAuth(addr, user, token, tls)
}

// proxyConfigFor returns the SOCKS5 proxy config for an account, or nil.
func proxyConfigFor(acct *accounts.Account) *imapwrap.ProxyConfig {
	if acct.ProxyHost == "" {
		return nil
	}
	return &imapwrap.ProxyConfig{
		Host:     acct.ProxyHost,
		Port:     acct.ProxyPort,
		Username: acct.ProxyUsername,
		Password: acct.ProxyPassword,
	}
}

// connectAccount dials the IMAP server with the appropriate auth method.
func (o *Orchestrator) connectAccount(acct *accounts.Account, addr string) (IMAPClient, error) {
	if acct.IsOAuth() {
		if o.tokenRefresh == nil {
			return nil, fmt.Errorf("oauth account %d but no token refresher configured", acct.ID)
		}
		token, err := o.tokenRefresh.EnsureFreshToken(context.Background(), acct.ID, acct.UserID)
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		return o.dialOAuth(addr, acct.Username, token, acct.UseTLS)
	}
	return o.dial(addr, acct.Username, acct.Password, acct.UseTLS, proxyConfigFor(acct))
}

func (o *Orchestrator) reloadFolder(accountID, folderID int64) *accounts.Folder {
	folders, err := o.accounts.ListFolders(accountID)
	if err != nil {
		return nil
	}
	for _, f := range folders {
		if f.ID == folderID {
			return f
		}
	}
	return nil
}

// Run backs up all enabled folders for the given account.
// If onProgress is non-nil, it is called before each folder sync.
func (o *Orchestrator) Run(accountID, userID int64, onProgress ProgressFunc) (*Result, error) {
	acct, err := o.accounts.GetByID(accountID, userID)
	if err != nil {
		return nil, fmt.Errorf("loading account: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", acct.Host, acct.Port)
	client, err := o.connectAccount(acct, addr)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer func() { _ = client.Close() }()

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
		if onProgress != nil {
			onProgress(Progress{
				Folder:       folder.Name,
				FolderIndex:  i + 1,
				FolderTotal:  len(enabled),
				NewMessages:  result.NewMessages,
				NewLocations: result.NewLocations,
			})
		}

		// Retry loop: keep syncing as long as forward progress is made.
		// On connection failure, reconnect and retry. Stop when no new
		// locations or envelopes are stored (no progress) or on non-connection errors.
		var accEnvs []imapwrap.Envelope
		for {
			prevLocs := result.NewLocations
			prevEnvs := result.NewEnvelopes
			syncErr := o.syncFolder(client, folder, userID, result, &accEnvs)
			if syncErr == nil {
				accEnvs = nil
				break
			}

			var ce *connError
			if !errors.As(syncErr, &ce) {
				result.Errors = append(result.Errors, fmt.Errorf("folder %q: %w", folder.Name, syncErr))
				accEnvs = nil
				break
			}

			if result.NewLocations == prevLocs && result.NewEnvelopes == prevEnvs {
				result.Errors = append(result.Errors, fmt.Errorf("folder %q: no progress, giving up: %w", folder.Name, syncErr))
				accEnvs = nil
				break
			}

			// Made progress — reconnect and retry.
			_ = client.Close()
			newClient, dialErr := o.connectAccount(acct, addr)
			if dialErr != nil {
				result.Errors = append(result.Errors, fmt.Errorf("folder %q reconnect: %w", folder.Name, dialErr))
				// Try once more so subsequent folders aren't stuck with a dead client.
				if c, err := o.connectAccount(acct, addr); err == nil {
					client = c
				}
				break
			}
			client = newClient

			// Reload folder to pick up LastSeenUID progress from partial run.
			if f := o.reloadFolder(accountID, folder.ID); f != nil {
				folder = f
			}
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
	envelopes *[]imapwrap.Envelope, // in/out: accumulated envelopes across retries
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

	// Merge prior envelopes (from previous retry attempts) with newly fetched ones.
	envMap := make(map[uint32]imapwrap.Envelope)
	for _, env := range *envelopes {
		envMap[env.UID] = env
	}

	// Start fetching from after the highest UID we already have envelopes for.
	fetchStartUID := folder.LastSeenUID + 1
	if len(*envelopes) > 0 {
		highest := (*envelopes)[len(*envelopes)-1].UID // sorted ascending
		if highest >= fetchStartUID {
			fetchStartUID = highest + 1
		}
	}

	// Fetch remaining envelopes (may return partial results on connection error).
	var envFetchErr error
	newEnvs, fetchErr := client.FetchEnvelopes(fetchStartUID, 0)
	if fetchErr != nil {
		envFetchErr = fetchErr
	}
	for _, env := range newEnvs {
		envMap[env.UID] = env
	}
	result.NewEnvelopes += len(newEnvs)

	// Flatten to sorted slice and write back for caller to accumulate.
	envs := make([]imapwrap.Envelope, 0, len(envMap))
	for _, env := range envMap {
		envs = append(envs, env)
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].UID < envs[j].UID })
	*envelopes = envs

	// If envelope fetch failed with nothing accumulated, signal connection error.
	if envFetchErr != nil && len(envs) == 0 {
		return &connError{err: fmt.Errorf("fetching envelopes: %w", envFetchErr)}
	}

	// Compute retention expunge set from all known messages (DB + new envelopes).
	// This must happen before the early return so that previously-backed-up
	// messages get cleaned up even when there are no new messages to fetch.
	// Skip retention when envelopes are incomplete — defer to next full sync.
	var expungeSet map[uint32]bool
	if envFetchErr == nil {
		var retentionErr error
		expungeSet, retentionErr = o.computeExpungeSet(folder, envs)
		if retentionErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("folder %q retention: %w", folder.Name, retentionErr))
			expungeSet = nil // disable incremental deletion on error
		}
	}

	// Track which UIDs are confirmed backed up (for gating deletion).
	backedUp := make(map[uint32]bool)
	existingLocs, _ := o.messages.ListLocationsByFolder(folder.ID)
	for _, loc := range existingLocs {
		backedUp[loc.UID] = true
	}

	// Mark-delete previously-backed-up messages that fall in the expunge set.
	var didDelete bool
	for uid := range expungeSet {
		if backedUp[uid] {
			if err := client.MarkDeleted([]uint32{uid}); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("mark deleted UID %d: %w", uid, err))
			} else {
				didDelete = true
			}
		}
	}

	if len(envs) == 0 {
		if didDelete {
			if err := client.Expunge(); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("expunge: %w", err))
			}
		}
		return nil
	}

	// Build list of UIDs that still need fetching (envMap already populated above).
	var toFetch []uint32
	var maxUID uint32
	var hadError bool

	for _, env := range envs {
		if exists, _ := o.messages.LocationExistsByFolderAndUID(folder.ID, env.UID); exists {
			if !hadError && env.UID > maxUID {
				maxUID = env.UID
			}
			continue
		}
		toFetch = append(toFetch, env.UID)
	}

	// Fetch bodies in batches to reduce round trips and connection fragility.
	var batchErr error
	for i := 0; i < len(toFetch); i += fetchBatchSize {
		end := i + fetchBatchSize
		if end > len(toFetch) {
			end = len(toFetch)
		}
		batch := toFetch[i:end]

		bodies, _, fetchErr := client.FetchBodies(batch)

		// Process each UID in order so hadError/maxUID stay consistent.
		for _, uid := range batch {
			body, ok := bodies[uid]
			if !ok {
				// When fetchErr is set, this UID was simply not received
				// before the connection died — don't log it individually
				// since the connection error below covers it.
				if fetchErr == nil {
					result.Errors = append(result.Errors, fmt.Errorf("UID %d: no message with UID %d", uid, uid))
				}
				hadError = true
				continue
			}
			env := envMap[uid]
			newMsg, storeErr := o.storeMessage(body, env, folder.ID, userID)
			if storeErr != nil {
				result.Errors = append(result.Errors, fmt.Errorf("UID %d store: %w", uid, storeErr))
				hadError = true
				continue
			}
			if !hadError && uid > maxUID {
				maxUID = uid
			}
			if newMsg {
				result.NewMessages++
			}
			result.NewLocations++

			// Incremental retention: message confirmed durable, delete from
			// server if the retention policy says so.
			if expungeSet[uid] {
				if err := client.MarkDeleted([]uint32{uid}); err != nil {
					result.Errors = append(result.Errors, fmt.Errorf("mark deleted UID %d: %w", uid, err))
				} else {
					didDelete = true
				}
			}
		}

		// Connection-level error: no point trying further batches.
		if fetchErr != nil {
			result.Errors = append(result.Errors, fmt.Errorf("fetching bodies (batch %d–%d, %d of %d received): %w",
				batch[0], batch[len(batch)-1], len(bodies), len(batch), fetchErr))
			batchErr = fetchErr
			break
		}
	}

	if maxUID > 0 {
		_ = o.accounts.SetLastSeenUID(folder.ID, maxUID)
	}

	if didDelete {
		if err := client.Expunge(); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("expunge: %w", err))
		}
	}

	if batchErr != nil {
		return &connError{err: batchErr}
	}
	if envFetchErr != nil {
		return &connError{err: fmt.Errorf("fetching envelopes: %w", envFetchErr)}
	}
	return nil
}

// computeExpungeSet merges existing DB locations with new envelopes and applies
// the retention policy to determine which UIDs should be deleted from the server.
func (o *Orchestrator) computeExpungeSet(
	folder *accounts.Folder,
	newEnvs []imapwrap.Envelope,
) (map[uint32]bool, error) {
	cfg, err := policy.ParseConfig(folder.PolicyJSON)
	if err != nil {
		return nil, fmt.Errorf("parsing policy: %w", err)
	}

	// Gather all known messages: existing locations from DB + new envelopes.
	locs, err := o.messages.ListLocationsByFolder(folder.ID)
	if err != nil {
		return nil, fmt.Errorf("listing locations: %w", err)
	}

	seen := make(map[uint32]bool, len(locs)+len(newEnvs))
	var msgs []policy.Message

	for _, loc := range locs {
		if seen[loc.UID] {
			continue
		}
		seen[loc.UID] = true
		m := policy.Message{UID: loc.UID}
		if loc.InternalDate != nil {
			m.InternalDate = *loc.InternalDate
		}
		msgs = append(msgs, m)
	}

	for _, env := range newEnvs {
		if seen[env.UID] {
			continue
		}
		seen[env.UID] = true
		msgs = append(msgs, policy.Message{UID: env.UID, InternalDate: env.Date})
	}

	uids := policy.Apply(cfg, msgs, time.Now())
	if len(uids) == 0 {
		return nil, nil
	}

	set := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		set[uid] = true
	}
	return set, nil
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
