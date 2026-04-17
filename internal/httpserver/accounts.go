package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/backup/policy"
	"github.com/hjiang/mnemosyne/internal/jobs"
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

	in, formErr := parseAccountForm(r)
	if formErr != "" {
		s.render(w, r, "accounts.html", map[string]any{"Title": "Accounts", "Error": formErr})
		return
	}

	acct, err := s.accounts.Create(userID, in.Label, in.Host, in.Port, in.Username, in.Password, in.UseTLS,
		in.ProxyHost, in.ProxyPort, in.ProxyUsername, in.ProxyPassword)
	if err != nil {
		s.render(w, r, "accounts.html", map[string]any{"Title": "Accounts", "Error": "Failed to create account."})
		return
	}

	// Auto-discover folders from the IMAP server.
	go s.discoverFolders(acct)

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

func (s *Server) accountFolders(w http.ResponseWriter, r *http.Request) {
	acct, _, ok := s.requireAccount(w, r)
	if !ok {
		return
	}

	// Sync folder list from the IMAP server (creates new folders, preserves existing).
	s.discoverFolders(acct)

	folders, err := s.accounts.ListFolders(acct.ID)
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
	acct, _, ok := s.requireAccount(w, r)
	if !ok {
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

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

func (s *Server) folderPolicyUpdate(w http.ResponseWriter, r *http.Request) {
	acct, _, ok := s.requireAccount(w, r)
	if !ok {
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

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

func (s *Server) folderResync(w http.ResponseWriter, r *http.Request) {
	acct, _, ok := s.requireAccount(w, r)
	if !ok {
		return
	}

	folderID, err := strconv.ParseInt(chi.URLParam(r, "folderID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if err := s.accounts.SetLastSeenUID(folderID, 0); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	acct, userID, ok := s.requireAccount(w, r)
	if !ok {
		return
	}

	payload, _ := json.Marshal(scheduler.BackupPayload{
		AccountID: acct.ID,
		UserID:    userID,
	})
	if _, err := s.queue.EnqueueIfNotActive("backup", string(payload), acct.ID); err != nil {
		if errors.Is(err, jobs.ErrJobActive) {
			s.render(w, r, "backup_result.html", map[string]any{
				"Title":  "Backup",
				"Status": "A backup job is already pending or running for this account.",
			})
			return
		}
		http.Error(w, "failed to enqueue backup job", http.StatusInternalServerError)
		return
	}

	s.render(w, r, "backup_result.html", map[string]any{
		"Title":  "Backup",
		"Status": "Backup job enqueued. It will run in the background.",
	})
}

func (s *Server) accountEdit(w http.ResponseWriter, r *http.Request) {
	acct, _, ok := s.requireAccount(w, r)
	if !ok {
		return
	}

	s.render(w, r, "account_edit.html", map[string]any{
		"Title":   fmt.Sprintf("Edit — %s", acct.Label),
		"Account": acct,
	})
}

func (s *Server) accountUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	acct, userID, ok := s.requireAccount(w, r)
	if !ok {
		return
	}

	in, formErr := parseAccountForm(r)
	if formErr != "" {
		s.render(w, r, "account_edit.html", map[string]any{"Title": "Edit Account", "Account": acct, "Error": formErr})
		return
	}

	// Keep existing passwords if the user left the fields blank.
	if in.Password == "" {
		in.Password = acct.Password
	}
	if in.ProxyPassword == "" {
		in.ProxyPassword = acct.ProxyPassword
	}

	if err := s.accounts.Update(acct.ID, userID, in.Label, in.Host, in.Port, in.Username, in.Password, in.UseTLS,
		in.ProxyHost, in.ProxyPort, in.ProxyUsername, in.ProxyPassword); err != nil {
		s.render(w, r, "account_edit.html", map[string]any{"Title": "Edit Account", "Account": acct, "Error": "Failed to update account."})
		return
	}

	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

// requireAccount parses the ":id" URL param and verifies ownership.
// On failure it writes 404 and returns ok=false.
func (s *Server) requireAccount(w http.ResponseWriter, r *http.Request) (*accounts.Account, int64, bool) {
	userID := auth.UserIDFromContext(r.Context())
	accountID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, 0, false
	}
	acct, err := s.accounts.GetByID(accountID, userID)
	if err != nil {
		http.NotFound(w, r)
		return nil, 0, false
	}
	return acct, userID, true
}

// accountFormInput holds the fields submitted by an IMAP-account form.
type accountFormInput struct {
	Label, Host, Username, Password        string
	Port                                   int
	UseTLS                                 bool
	ProxyHost, ProxyUsername, ProxyPassword string
	ProxyPort                              int
}

// parseAccountForm reads account form fields from the request.
// Returns a user-facing error string on invalid input; empty means success.
func parseAccountForm(r *http.Request) (*accountFormInput, string) {
	in := &accountFormInput{
		Label:         r.FormValue("label"),
		Host:          r.FormValue("host"),
		Username:      r.FormValue("username"),
		Password:      r.FormValue("password"),
		UseTLS:        r.FormValue("use_tls") == "on",
		ProxyHost:     r.FormValue("proxy_host"),
		ProxyUsername: r.FormValue("proxy_username"),
		ProxyPassword: r.FormValue("proxy_password"),
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil {
		return nil, "Invalid port."
	}
	in.Port = port
	if s := r.FormValue("proxy_port"); s != "" {
		p, err := strconv.Atoi(s)
		if err != nil {
			return nil, "Invalid proxy port."
		}
		in.ProxyPort = p
	}
	return in, ""
}

func (s *Server) discoverFolders(acct *accounts.Account) {
	addr := fmt.Sprintf("%s:%d", acct.Host, acct.Port)

	var proxyConf *imapwrap.ProxyConfig
	if acct.ProxyHost != "" {
		proxyConf = &imapwrap.ProxyConfig{
			Host:     acct.ProxyHost,
			Port:     acct.ProxyPort,
			Username: acct.ProxyUsername,
			Password: acct.ProxyPassword,
		}
	}

	client, err := imapwrap.Dial(addr, acct.Username, acct.Password, acct.UseTLS, proxyConf)
	if err != nil {
		log.Printf("folder discovery for account %d: connect failed: %q", acct.ID, err) //nolint:gosec // accountID is int, err is quoted
		return
	}
	defer client.Close() //nolint:errcheck

	names, err := client.ListFolders()
	if err != nil {
		log.Printf("folder discovery for account %d: list failed: %q", acct.ID, err) //nolint:gosec // accountID is int, err is quoted
		return
	}

	for _, name := range names {
		if _, err := s.accounts.CreateFolder(acct.ID, name); err != nil {
			log.Printf("folder discovery for account %d: creating %q: %q", acct.ID, name, err) //nolint:gosec // all values quoted
		}
	}
	log.Printf("folder discovery for account %d: found %d folders", acct.ID, len(names)) //nolint:gosec // no untrusted input
}
