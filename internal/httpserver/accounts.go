package httpserver

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/auth"
)

func (s *Server) accountsList(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	accts, err := s.accounts.List(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "accounts.html", map[string]any{"Title": "Accounts", "Accounts": accts})
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
		s.render(w, "accounts.html", map[string]any{"Title": "Accounts", "Error": "Invalid port."})
		return
	}

	_, err = s.accounts.Create(userID, label, host, port, username, password, useTLS)
	if err != nil {
		s.render(w, "accounts.html", map[string]any{"Title": "Accounts", "Error": "Failed to create account."})
		return
	}

	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
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

	folders, err := s.accounts.ListFolders(accountID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.render(w, "folders.html", map[string]any{
		"Title":   fmt.Sprintf("Folders — %s", acct.Label),
		"Account": acct,
		"Folders": folders,
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

	result, runErr := s.backup.Run(accountID, userID)
	var status string
	switch {
	case runErr != nil:
		status = fmt.Sprintf("Backup failed: %v", runErr)
	case len(result.Errors) > 0:
		status = fmt.Sprintf("Backup finished with %d errors. %d new messages.", len(result.Errors), result.NewMessages)
	default:
		status = fmt.Sprintf("Backup complete. %d new messages.", result.NewMessages)
	}

	s.render(w, "backup_result.html", map[string]any{
		"Title":  "Backup Result",
		"Status": status,
	})
}
