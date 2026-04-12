// Package accounts manages IMAP account CRUD and credential encryption.
package accounts

import (
	"database/sql"
	"errors"
	"fmt"
)

var (
	// ErrNotFound indicates the requested account or folder was not found.
	ErrNotFound = errors.New("not found")
)

// Account represents an IMAP account.
type Account struct {
	ID         int64
	UserID     int64
	Label      string
	Host       string
	Port       int
	Username   string
	Password   string // decrypted
	UseTLS     bool
	LastSyncAt *int64
}

// Folder represents an IMAP folder within an account.
type Folder struct {
	ID          int64
	AccountID   int64
	Name        string
	Enabled     bool
	UIDValidity *uint32
	LastSeenUID uint32
	PolicyJSON  string
}

// Repo manages IMAP accounts and folders in SQLite.
type Repo struct {
	db *sql.DB
	km *KeyManager
}

// NewRepo creates an accounts repository.
func NewRepo(db *sql.DB, km *KeyManager) *Repo {
	return &Repo{db: db, km: km}
}

// Create inserts a new IMAP account. The password is encrypted before storage.
// enforces user isolation
func (r *Repo) Create(userID int64, label, host string, port int, username, password string, useTLS bool) (*Account, error) {
	enc, err := r.km.Encrypt([]byte(password))
	if err != nil {
		return nil, fmt.Errorf("encrypting password: %w", err)
	}

	res, err := r.db.Exec(
		`INSERT INTO imap_accounts (user_id, label, host, port, username, password_enc, use_tls)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, label, host, port, username, enc, boolToInt(useTLS),
	)
	if err != nil {
		return nil, fmt.Errorf("inserting account: %w", err)
	}

	id, _ := res.LastInsertId()
	return &Account{
		ID: id, UserID: userID, Label: label,
		Host: host, Port: port, Username: username,
		Password: password, UseTLS: useTLS,
	}, nil
}

// GetByID retrieves an account by ID, scoped to the given user.
// enforces user isolation
func (r *Repo) GetByID(id, userID int64) (*Account, error) {
	var a Account
	var encPwd []byte
	var tls int
	err := r.db.QueryRow(
		`SELECT id, user_id, label, host, port, username, password_enc, use_tls, last_sync_at
		 FROM imap_accounts WHERE id = ? AND user_id = ?`,
		id, userID,
	).Scan(&a.ID, &a.UserID, &a.Label, &a.Host, &a.Port, &a.Username, &encPwd, &tls, &a.LastSyncAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("querying account: %w", err)
	}

	pwd, err := r.km.Decrypt(encPwd)
	if err != nil {
		return nil, fmt.Errorf("decrypting password: %w", err)
	}
	a.Password = string(pwd)
	a.UseTLS = tls == 1
	return &a, nil
}

// List returns all accounts for a user.
// enforces user isolation
func (r *Repo) List(userID int64) ([]*Account, error) {
	rows, err := r.db.Query(
		`SELECT id, user_id, label, host, port, username, password_enc, use_tls, last_sync_at
		 FROM imap_accounts WHERE user_id = ?`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var accounts []*Account
	for rows.Next() {
		var a Account
		var encPwd []byte
		var tls int
		if err := rows.Scan(&a.ID, &a.UserID, &a.Label, &a.Host, &a.Port, &a.Username, &encPwd, &tls, &a.LastSyncAt); err != nil {
			return nil, fmt.Errorf("scanning account: %w", err)
		}
		pwd, err := r.km.Decrypt(encPwd)
		if err != nil {
			return nil, fmt.Errorf("decrypting password: %w", err)
		}
		a.Password = string(pwd)
		a.UseTLS = tls == 1
		accounts = append(accounts, &a)
	}
	return accounts, rows.Err()
}

// CreateFolder inserts a folder for the given account.
func (r *Repo) CreateFolder(accountID int64, name string) (*Folder, error) {
	res, err := r.db.Exec(
		`INSERT INTO imap_folders (account_id, name) VALUES (?, ?)
		 ON CONFLICT(account_id, name) DO NOTHING`,
		accountID, name,
	)
	if err != nil {
		return nil, fmt.Errorf("inserting folder: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Folder{ID: id, AccountID: accountID, Name: name, PolicyJSON: `{"leave_on_server":"all"}`}, nil
}

// ListFolders returns all folders for an account.
func (r *Repo) ListFolders(accountID int64) ([]*Folder, error) {
	rows, err := r.db.Query(
		`SELECT id, account_id, name, enabled, uid_validity, last_seen_uid, policy_json
		 FROM imap_folders WHERE account_id = ?`,
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing folders: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var folders []*Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.AccountID, &f.Name, &f.Enabled, &f.UIDValidity, &f.LastSeenUID, &f.PolicyJSON); err != nil {
			return nil, fmt.Errorf("scanning folder: %w", err)
		}
		folders = append(folders, &f)
	}
	return folders, rows.Err()
}

// SetFolderEnabled toggles a folder's backup enabled flag.
func (r *Repo) SetFolderEnabled(folderID int64, enabled bool) error {
	_, err := r.db.Exec("UPDATE imap_folders SET enabled = ? WHERE id = ?", boolToInt(enabled), folderID)
	if err != nil {
		return fmt.Errorf("updating folder enabled: %w", err)
	}
	return nil
}

// SetUIDValidity updates the UIDVALIDITY for a folder.
func (r *Repo) SetUIDValidity(folderID int64, uidValidity uint32) error {
	_, err := r.db.Exec("UPDATE imap_folders SET uid_validity = ? WHERE id = ?", uidValidity, folderID)
	if err != nil {
		return fmt.Errorf("updating uid_validity: %w", err)
	}
	return nil
}

// SetLastSeenUID updates the last seen UID for a folder.
func (r *Repo) SetLastSeenUID(folderID int64, uid uint32) error {
	_, err := r.db.Exec("UPDATE imap_folders SET last_seen_uid = ? WHERE id = ?", uid, folderID)
	if err != nil {
		return fmt.Errorf("updating last_seen_uid: %w", err)
	}
	return nil
}

// SetLastSyncAt records when the account was last synced.
func (r *Repo) SetLastSyncAt(accountID int64, ts int64) error {
	_, err := r.db.Exec("UPDATE imap_accounts SET last_sync_at = ? WHERE id = ?", ts, accountID)
	if err != nil {
		return fmt.Errorf("updating last_sync_at: %w", err)
	}
	return nil
}

// EnabledAccount summarizes an account that has at least one enabled folder.
type EnabledAccount struct {
	AccountID int64
	UserID    int64
}

// ListAllEnabled returns all accounts that have at least one enabled folder.
// This is used by the scheduler to enqueue backup jobs across all users.
func (r *Repo) ListAllEnabled() ([]EnabledAccount, error) {
	rows, err := r.db.Query(
		`SELECT DISTINCT a.id, a.user_id
		 FROM imap_accounts a
		 JOIN imap_folders f ON f.account_id = a.id
		 WHERE f.enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("listing enabled accounts: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var result []EnabledAccount
	for rows.Next() {
		var ea EnabledAccount
		if err := rows.Scan(&ea.AccountID, &ea.UserID); err != nil {
			return nil, fmt.Errorf("scanning: %w", err)
		}
		result = append(result, ea)
	}
	return result, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
