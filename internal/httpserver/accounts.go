package httpserver

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/backup/policy"
	"github.com/hjiang/mnemosyne/internal/scheduler"
)

func (s *Server) accountsList(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	accts, err := s.accounts.List(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "accounts.html", map[string]any{"Title": "Accounts", "Accounts": accts})
}

func (s *Server) accountCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	userID := auth.UserIDFromContext(r.Context())

	label := r.FormValue("label")
	host := r.FormValue("host")
	portStr := r.FormValue("port")
	username := r.FormValue("username")
	password := r.FormValue("password")
	useTLS := r.FormValue("use_tls") == "on"

	port, err := strconv.Atoi(portStr)
	if err != nil {
		s.render(w, r, "accounts.html", map[string]any{"Title": "Accounts", "Error": "Invalid port."})
		return
	}

	acct, err := s.accounts.Create(userID, label, host, port, username, password, useTLS)
	if err != nil {
		s.render(w, r, "accounts.html", map[string]any{"Title": "Accounts", "Error": "Failed to create account."})
		return
	}

	// Auto-discover folders from the IMAP server.
	go s.discoverFolders(acct.ID, host, port, username, password, useTLS)

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

func (s *Server) accountFolders(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	accountID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	acct, err := s.accounts.GetByID(accountID, userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Sync folder list from the IMAP server (creates new folders, preserves existing).
	s.discoverFolders(accountID, acct.Host, acct.Port, acct.Username, acct.Password, acct.UseTLS)

	folders, err := s.accounts.ListFolders(accountID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Parse policy JSON for each folder so the template can render form state.
	type folderView struct {
		*accounts.Folder
		PolicyType string
		PolicyN    int
		PolicyDays int
	}
	views := make([]folderView, len(folders))
	for i, f := range folders {
		fv := folderView{Folder: f, PolicyType: "all"}
		if cfg, err := policy.ParseConfig(f.PolicyJSON); err == nil {
			fv.PolicyType = cfg.LeaveOnServer
			fv.PolicyN = cfg.N
			fv.PolicyDays = cfg.Days
		}
		views[i] = fv
	}

	s.render(w, r, "folders.html", map[string]any{
		"Title":   fmt.Sprintf("Folders — %s", acct.Label),
		"Account": acct,
		"Folders": views,
	})
}

func (s *Server) folderToggle(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	accountID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Verify account ownership.
	if _, err := s.accounts.GetByID(accountID, userID); err != nil {
		http.NotFound(w, r)
		return
	}

	folderID, err := strconv.ParseInt(chi.URLParam(r, "folderID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	enabled := r.FormValue("enabled") == "on"
	if err := s.accounts.SetFolderEnabled(folderID, enabled); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", accountID), http.StatusSeeOther)
}

func (s *Server) folderPolicyUpdate(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	accountID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Verify account ownership.
	if _, err := s.accounts.GetByID(accountID, userID); err != nil {
		http.NotFound(w, r)
		return
	}

	folderID, err := strconv.ParseInt(chi.URLParam(r, "folderID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	policyType := r.FormValue("policy_type")

	cfg := policy.Config{LeaveOnServer: policyType}
	switch policyType {
	case "all":
		// No extra fields needed.
	case "newest_n":
		n, err := strconv.Atoi(r.FormValue("policy_n"))
		if err != nil || n < 1 {
			http.Error(w, "N must be a positive integer", http.StatusBadRequest)
			return
		}
		cfg.N = n
	case "younger_than":
		days, err := strconv.Atoi(r.FormValue("policy_days"))
		if err != nil || days < 1 {
			http.Error(w, "Days must be a positive integer", http.StatusBadRequest)
			return
		}
		cfg.Days = days
	default:
		http.Error(w, "Invalid policy type", http.StatusBadRequest)
		return
	}

	policyJSON, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.accounts.SetFolderPolicy(folderID, string(policyJSON)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", accountID), http.StatusSeeOther)
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	accountID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Verify account ownership.
	if _, err := s.accounts.GetByID(accountID, userID); err != nil {
		http.NotFound(w, r)
		return
	}

	payload, _ := json.Marshal(scheduler.BackupPayload{
		AccountID: accountID,
		UserID:    userID,
	})
	if _, err := s.queue.Enqueue("backup", string(payload)); err != nil {
		http.Error(w, "failed to enqueue backup job", http.StatusInternalServerError)
		return
	}

	s.render(w, r, "backup_result.html", map[string]any{
		"Title":  "Backup",
		"Status": "Backup job enqueued. It will run in the background.",
	})
}

func (s *Server) discoverFolders(accountID int64, host string, port int, username, password string, useTLS bool) {
	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := imapwrap.Dial(addr, username, password, useTLS)
	if err != nil {
		log.Printf("folder discovery for account %d: connect failed: %q", accountID, err) //nolint:gosec // accountID is int, err is quoted
		return
	}
	defer client.Close() //nolint:errcheck

	names, err := client.ListFolders()
	if err != nil {
		log.Printf("folder discovery for account %d: list failed: %q", accountID, err) //nolint:gosec // accountID is int, err is quoted
		return
	}

	for _, name := range names {
		if _, err := s.accounts.CreateFolder(accountID, name); err != nil {
			log.Printf("folder discovery for account %d: creating %q: %q", accountID, name, err) //nolint:gosec // all values quoted
		}
	}
	log.Printf("folder discovery for account %d: found %d folders", accountID, len(names)) //nolint:gosec // no untrusted input
}
