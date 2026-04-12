package auth

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrSessionNotFound indicates the session ID was not found in the store.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionExpired indicates the session has passed its expiry time.
	ErrSessionExpired = errors.New("session expired")
)

// Session represents an authenticated user session.
type Session struct {
	ID        []byte
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionStore manages sessions in SQLite.
type SessionStore struct {
	db  *sql.DB
	now func() time.Time
	ttl time.Duration
}

// NewSessionStore creates a session store. The now function controls time for
// testing; pass time.Now in production.
func NewSessionStore(db *sql.DB, now func() time.Time, ttl time.Duration) *SessionStore {
	return &SessionStore{db: db, now: now, ttl: ttl}
}

// Create generates a new session for the given user.
func (s *SessionStore) Create(userID int64) (*Session, error) {
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("generating session ID: %w", err)
	}

	now := s.now()
	expiresAt := now.Add(s.ttl)

	_, err := s.db.Exec(
		"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		id, userID, now.Unix(), expiresAt.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("inserting session: %w", err)
	}

	return &Session{
		ID:        id,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}, nil
}

// Lookup retrieves a session by ID. Returns ErrSessionNotFound if the ID is
// unknown, or ErrSessionExpired if the session is past its expiry time.
func (s *SessionStore) Lookup(id []byte) (*Session, error) {
	var (
		userID    int64
		createdAt int64
		expiresAt int64
	)
	err := s.db.QueryRow(
		"SELECT user_id, created_at, expires_at FROM sessions WHERE id = ?",
		id,
	).Scan(&userID, &createdAt, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("looking up session: %w", err)
	}

	sess := &Session{
		ID:        id,
		UserID:    userID,
		CreatedAt: time.Unix(createdAt, 0),
		ExpiresAt: time.Unix(expiresAt, 0),
	}

	if s.now().After(sess.ExpiresAt) {
		// Best-effort cleanup of the expired session.
		_ = s.Delete(id)
		return nil, ErrSessionExpired
	}

	return sess, nil
}

// Delete removes a session by ID.
func (s *SessionStore) Delete(id []byte) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}
