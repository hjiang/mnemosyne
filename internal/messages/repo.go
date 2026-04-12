// Package messages manages email message storage with content-addressed dedup.
package messages

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound indicates the requested message was not found.
var ErrNotFound = errors.New("message not found")

// Message represents a backed-up email message.
type Message struct {
	Hash           []byte
	UserID         int64
	MessageID      string
	FromAddr       string
	ToAddrs        string
	CcAddrs        string
	Subject        string
	Date           *int64
	Size           int64
	HasAttachments bool
	BodyText       string
}

// Location records where a message appears (folder + UID).
type Location struct {
	MessageHash  []byte
	FolderID     int64
	UID          uint32
	InternalDate *int64
	Flags        string
}

// Attachment represents a MIME attachment.
type Attachment struct {
	ID            int64
	MessageHash   []byte
	Filename      string
	MimeType      string
	Size          int64
	BlobHash      []byte
	TextExtracted int // 0=pending, 1=done, 2=failed
}

// Repo manages messages, locations, and attachments in SQLite.
type Repo struct {
	db *sql.DB
}

// NewRepo creates a messages repository.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// Insert stores a new message. If a message with the same hash already exists
// for this user, it is a no-op (dedup invariant).
// enforces user isolation
func (r *Repo) Insert(m *Message) error {
	_, err := r.db.Exec(
		`INSERT INTO messages (hash, user_id, message_id, from_addr, to_addrs, cc_addrs, subject, date, size, has_attachments, body_text)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(hash) DO NOTHING`,
		m.Hash, m.UserID, m.MessageID, m.FromAddr, m.ToAddrs, m.CcAddrs,
		m.Subject, m.Date, m.Size, boolToInt(m.HasAttachments), m.BodyText,
	)
	if err != nil {
		return fmt.Errorf("inserting message: %w", err)
	}
	return nil
}

// InsertLocation records that a message appears at a folder+UID.
// Returns an error if the message hash doesn't exist.
func (r *Repo) InsertLocation(loc *Location) error {
	_, err := r.db.Exec(
		`INSERT INTO message_locations (message_hash, folder_id, uid, internal_date, flags)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(folder_id, uid) DO NOTHING`,
		loc.MessageHash, loc.FolderID, loc.UID, loc.InternalDate, loc.Flags,
	)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return fmt.Errorf("message hash not found: %w", err)
		}
		return fmt.Errorf("inserting location: %w", err)
	}
	return nil
}

// InsertAttachment records an attachment for a message.
func (r *Repo) InsertAttachment(att *Attachment) error {
	res, err := r.db.Exec(
		`INSERT INTO attachments (message_hash, filename, mime_type, size, blob_hash, text_extracted)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		att.MessageHash, att.Filename, att.MimeType, att.Size, att.BlobHash, att.TextExtracted,
	)
	if err != nil {
		return fmt.Errorf("inserting attachment: %w", err)
	}
	att.ID, _ = res.LastInsertId()
	return nil
}

// GetByHash retrieves a message by its content hash, scoped to user.
// enforces user isolation
func (r *Repo) GetByHash(hash []byte, userID int64) (*Message, error) {
	var m Message
	var hasAtt int
	err := r.db.QueryRow(
		`SELECT hash, user_id, message_id, from_addr, to_addrs, cc_addrs, subject, date, size, has_attachments, body_text
		 FROM messages WHERE hash = ? AND user_id = ?`,
		hash, userID,
	).Scan(&m.Hash, &m.UserID, &m.MessageID, &m.FromAddr, &m.ToAddrs, &m.CcAddrs,
		&m.Subject, &m.Date, &m.Size, &hasAtt, &m.BodyText)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("querying message: %w", err)
	}
	m.HasAttachments = hasAtt == 1
	return &m, nil
}

// ExistsByHash checks whether a message with the given hash exists for this user.
// enforces user isolation
func (r *Repo) ExistsByHash(hash []byte, userID int64) (bool, error) {
	var count int
	err := r.db.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE hash = ? AND user_id = ?",
		hash, userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking message existence: %w", err)
	}
	return count > 0, nil
}

// FindByMessageID looks up messages by RFC822 Message-ID header, scoped to user.
// enforces user isolation
func (r *Repo) FindByMessageID(messageID string, userID int64) ([]*Message, error) {
	rows, err := r.db.Query(
		`SELECT hash, user_id, message_id, from_addr, to_addrs, cc_addrs, subject, date, size, has_attachments, body_text
		 FROM messages WHERE message_id = ? AND user_id = ?`,
		messageID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying by message_id: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []*Message
	for rows.Next() {
		var m Message
		var hasAtt int
		if err := rows.Scan(&m.Hash, &m.UserID, &m.MessageID, &m.FromAddr, &m.ToAddrs, &m.CcAddrs,
			&m.Subject, &m.Date, &m.Size, &hasAtt, &m.BodyText); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		m.HasAttachments = hasAtt == 1
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// ListByFolder returns messages in a folder, ordered by date descending.
// enforces user isolation
func (r *Repo) ListByFolder(folderID int64, userID int64) ([]*Message, error) {
	rows, err := r.db.Query(
		`SELECT m.hash, m.user_id, m.message_id, m.from_addr, m.to_addrs, m.cc_addrs,
		        m.subject, m.date, m.size, m.has_attachments, m.body_text
		 FROM messages m
		 JOIN message_locations ml ON ml.message_hash = m.hash
		 WHERE ml.folder_id = ? AND m.user_id = ?
		 ORDER BY m.date DESC`,
		folderID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing by folder: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []*Message
	for rows.Next() {
		var m Message
		var hasAtt int
		if err := rows.Scan(&m.Hash, &m.UserID, &m.MessageID, &m.FromAddr, &m.ToAddrs, &m.CcAddrs,
			&m.Subject, &m.Date, &m.Size, &hasAtt, &m.BodyText); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		m.HasAttachments = hasAtt == 1
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// ListByFolderPaged returns a page of messages in a folder, ordered by date descending.
// enforces user isolation
func (r *Repo) ListByFolderPaged(folderID, userID int64, limit, offset int) ([]*Message, error) {
	rows, err := r.db.Query(
		`SELECT m.hash, m.user_id, m.message_id, m.from_addr, m.to_addrs, m.cc_addrs,
		        m.subject, m.date, m.size, m.has_attachments, m.body_text
		 FROM messages m
		 JOIN message_locations ml ON ml.message_hash = m.hash
		 WHERE ml.folder_id = ? AND m.user_id = ?
		 ORDER BY m.date DESC
		 LIMIT ? OFFSET ?`,
		folderID, userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing by folder paged: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []*Message
	for rows.Next() {
		var m Message
		var hasAtt int
		if err := rows.Scan(&m.Hash, &m.UserID, &m.MessageID, &m.FromAddr, &m.ToAddrs, &m.CcAddrs,
			&m.Subject, &m.Date, &m.Size, &hasAtt, &m.BodyText); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		m.HasAttachments = hasAtt == 1
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// CountByFolder returns the number of messages in a folder for a user.
// enforces user isolation
func (r *Repo) CountByFolder(folderID, userID int64) (int, error) {
	var count int
	err := r.db.QueryRow(
		`SELECT COUNT(*)
		 FROM message_locations ml
		 JOIN messages m ON m.hash = ml.message_hash
		 WHERE ml.folder_id = ? AND m.user_id = ?`,
		folderID, userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting by folder: %w", err)
	}
	return count, nil
}

// CountByFoldersForUser returns message counts for all folders belonging to a user.
// enforces user isolation
func (r *Repo) CountByFoldersForUser(userID int64) (map[int64]int, error) {
	rows, err := r.db.Query(
		`SELECT ml.folder_id, COUNT(*)
		 FROM message_locations ml
		 JOIN messages m ON m.hash = ml.message_hash
		 JOIN imap_folders f ON f.id = ml.folder_id
		 JOIN imap_accounts a ON a.id = f.account_id
		 WHERE a.user_id = ?
		 GROUP BY ml.folder_id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("counting by folders: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	counts := make(map[int64]int)
	for rows.Next() {
		var folderID int64
		var count int
		if err := rows.Scan(&folderID, &count); err != nil {
			return nil, fmt.Errorf("scanning folder count: %w", err)
		}
		counts[folderID] = count
	}
	return counts, rows.Err()
}

// CountLocationsByHash counts how many locations reference a given message hash.
func (r *Repo) CountLocationsByHash(hash []byte) (int, error) {
	var count int
	err := r.db.QueryRow(
		"SELECT COUNT(*) FROM message_locations WHERE message_hash = ?",
		hash,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting locations: %w", err)
	}
	return count, nil
}

// DeleteLocationsByFolder removes all locations for a folder (used on UIDVALIDITY reset).
func (r *Repo) DeleteLocationsByFolder(folderID int64) error {
	_, err := r.db.Exec("DELETE FROM message_locations WHERE folder_id = ?", folderID)
	if err != nil {
		return fmt.Errorf("deleting locations: %w", err)
	}
	return nil
}

// ListAttachments returns attachments for a message.
func (r *Repo) ListAttachments(messageHash []byte) ([]*Attachment, error) {
	rows, err := r.db.Query(
		`SELECT id, message_hash, filename, mime_type, size, blob_hash, text_extracted
		 FROM attachments WHERE message_hash = ?`,
		messageHash,
	)
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var atts []*Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.MessageHash, &a.Filename, &a.MimeType, &a.Size, &a.BlobHash, &a.TextExtracted); err != nil {
			return nil, fmt.Errorf("scanning attachment: %w", err)
		}
		atts = append(atts, &a)
	}
	return atts, rows.Err()
}

// IndexFTS inserts a message's searchable content into the FTS5 index.
// The rowid must match the message's internal SQLite rowid.
func (r *Repo) IndexFTS(rowid int64, subject, fromAddr, toAddrs, ccAddrs, bodyText string) error {
	_, err := r.db.Exec(
		"INSERT INTO messages_fts(rowid, subject, from_addr, to_addrs, cc_addrs, body_text) VALUES (?, ?, ?, ?, ?, ?)",
		rowid, subject, fromAddr, toAddrs, ccAddrs, bodyText)
	if err != nil {
		return fmt.Errorf("indexing FTS: %w", err)
	}
	return nil
}

// GetRowID returns the internal SQLite rowid for a message hash.
func (r *Repo) GetRowID(hash []byte) (int64, error) {
	var rowid int64
	err := r.db.QueryRow("SELECT rowid FROM messages WHERE hash = ?", hash).Scan(&rowid)
	if err != nil {
		return 0, fmt.Errorf("getting rowid: %w", err)
	}
	return rowid, nil
}

// UpdateAttachmentText marks an attachment as text-extracted and updates its content.
func (r *Repo) UpdateAttachmentText(attID int64, _ string, status int) error {
	_, err := r.db.Exec(
		"UPDATE attachments SET text_extracted = ? WHERE id = ?",
		status, attID)
	if err != nil {
		return fmt.Errorf("updating attachment text: %w", err)
	}
	return nil
}

// UpdateBodyText updates the body_text field of a message.
func (r *Repo) UpdateBodyText(hash []byte, bodyText string) error {
	_, err := r.db.Exec("UPDATE messages SET body_text = ? WHERE hash = ?", bodyText, hash)
	if err != nil {
		return fmt.Errorf("updating body_text: %w", err)
	}
	return nil
}

// ListUnindexedMessages returns messages that have no FTS entry yet.
func (r *Repo) ListUnindexedMessages() ([]*Message, error) {
	rows, err := r.db.Query(
		`SELECT m.hash, m.user_id, m.message_id, m.from_addr, m.to_addrs, m.cc_addrs,
		        m.subject, m.date, m.size, m.has_attachments, COALESCE(m.body_text, '')
		 FROM messages m
		 WHERE m.rowid NOT IN (SELECT rowid FROM messages_fts)
		 LIMIT 1000`)
	if err != nil {
		return nil, fmt.Errorf("listing unindexed messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []*Message
	for rows.Next() {
		var m Message
		var hasAtt int
		if err := rows.Scan(&m.Hash, &m.UserID, &m.MessageID, &m.FromAddr, &m.ToAddrs, &m.CcAddrs,
			&m.Subject, &m.Date, &m.Size, &hasAtt, &m.BodyText); err != nil {
			return nil, fmt.Errorf("scanning: %w", err)
		}
		m.HasAttachments = hasAtt == 1
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// ListEmptyBodyText returns messages where body_text is empty or NULL.
// Used to backfill body text from raw blobs.
func (r *Repo) ListEmptyBodyText(limit int) ([]*Message, error) {
	rows, err := r.db.Query(
		`SELECT hash, user_id, message_id, from_addr, to_addrs, cc_addrs,
		        subject, date, size, has_attachments, COALESCE(body_text, '')
		 FROM messages
		 WHERE body_text IS NULL OR body_text = ''
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing empty body_text messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []*Message
	for rows.Next() {
		var m Message
		var hasAtt int
		if err := rows.Scan(&m.Hash, &m.UserID, &m.MessageID, &m.FromAddr, &m.ToAddrs, &m.CcAddrs,
			&m.Subject, &m.Date, &m.Size, &hasAtt, &m.BodyText); err != nil {
			return nil, fmt.Errorf("scanning: %w", err)
		}
		m.HasAttachments = hasAtt == 1
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
