// Package users manages user accounts and persistence.
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotFound indicates the requested user was not found.
	ErrNotFound = errors.New("user not found")
	// ErrDuplicateEmail indicates the email address is already registered.
	ErrDuplicateEmail = errors.New("email already exists")
)

// User represents an application user.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// Repo manages users in SQLite.
type Repo struct {
	db  *sql.DB
	now func() time.Time
}

// NewRepo creates a user repository.
func NewRepo(db *sql.DB, now func() time.Time) *Repo {
	return &Repo{db: db, now: now}
}

// Create inserts a new user. Returns ErrDuplicateEmail if the email is taken.
func (r *Repo) Create(email, passwordHash string) (*User, error) {
	now := r.now()
	res, err := r.db.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)",
		email, passwordHash, now.Unix(),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrDuplicateEmail
		}
		return nil, fmt.Errorf("inserting user: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("getting user ID: %w", err)
	}

	return &User{
		ID:           id,
		Email:        email,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	}, nil
}

// GetByEmail retrieves a user by email (case-insensitive).
// enforces user isolation
func (r *Repo) GetByEmail(email string) (*User, error) {
	var u User
	var createdAt int64
	err := r.db.QueryRow(
		"SELECT id, email, password_hash, created_at FROM users WHERE email = ?",
		email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("querying user by email: %w", err)
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	return &u, nil
}

// GetByID retrieves a user by ID.
// enforces user isolation
func (r *Repo) GetByID(id int64) (*User, error) {
	var u User
	var createdAt int64
	err := r.db.QueryRow(
		"SELECT id, email, password_hash, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("querying user by id: %w", err)
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	return &u, nil
}
