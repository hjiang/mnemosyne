// Package httpserver implements the HTTP handlers and routing.
package httpserver

import (
	"embed"
	"encoding/hex"
	"html/template"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/jobs"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/oauth"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server holds HTTP handler dependencies.
type Server struct {
	router    chi.Router
	templates map[string]*template.Template
	users     *users.Repo
	sessions  *auth.SessionStore
	accounts  *accounts.Repo
	backup    *backup.Orchestrator
	queue     *jobs.Queue
	messages  *messages.Repo
	search    *search.Executor
	blobs     *blobs.Store
	tokenMgr  *oauth.TokenManager
}

// New creates an HTTP server with all routes wired.
// acctRepo and orch may be nil if IMAP features are not yet configured.
// tokenMgr may be nil when OAuth is not configured.
func New(userRepo *users.Repo, sessions *auth.SessionStore, acctRepo *accounts.Repo, orch *backup.Orchestrator, jobQueue *jobs.Queue, msgRepo *messages.Repo, searchExec *search.Executor, blobStore *blobs.Store, tokenMgr *oauth.TokenManager) *Server {
	funcMap := template.FuncMap{
		"hexhash": hex.EncodeToString,
	}
	parsePage := func(name string) *template.Template {
		return template.Must(template.New("layout.html").Funcs(funcMap).ParseFS(
			templateFS, "templates/layout.html", "templates/"+name))
	}
	parseStandalone := func(name string) *template.Template {
		return template.Must(template.New(name).Funcs(funcMap).ParseFS(templateFS, "templates/"+name))
	}
	templates := map[string]*template.Template{
		"login.html":         parseStandalone("login.html"),
		"home.html":          parsePage("home.html"),
		"accounts.html":      parsePage("accounts.html"),
		"folders.html":       parsePage("folders.html"),
		"backup_result.html": parsePage("backup_result.html"),
		"search.html":        parsePage("search.html"),
		"message.html":       parsePage("message.html"),
		"backups.html":       parsePage("backups.html"),
		"backup_detail.html": parsePage("backup_detail.html"),
		"browse.html": template.Must(template.New("layout.html").Funcs(funcMap).ParseFS(
			templateFS, "templates/layout.html", "templates/browse.html", "templates/browse_messages.html")),
		"browse_messages.html": parseStandalone("browse_messages.html"),
		"account_edit.html":    parsePage("account_edit.html"),
	}

	s := &Server{
		router:    chi.NewRouter(),
		templates: templates,
		users:     userRepo,
		sessions:  sessions,
		accounts:  acctRepo,
		backup:    orch,
		queue:     jobQueue,
		messages:  msgRepo,
		search:    searchExec,
		blobs:     blobStore,
		tokenMgr:  tokenMgr,
	}

	s.router.Handle("/static/*", http.FileServer(http.FS(staticFS)))

	s.router.Get("/login", s.loginForm)
	s.router.Post("/login", s.loginSubmit)
	s.router.Post("/logout", s.logout)

	s.router.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(sessions, "/login"))
		r.Get("/", s.home)
		r.Get("/accounts", s.accountsList)
		r.Post("/accounts", s.accountCreate)
		r.Get("/accounts/{id}/edit", s.accountEdit)
		r.Post("/accounts/{id}/edit", s.accountUpdate)
		r.Get("/accounts/{id}/folders", s.accountFolders)
		r.Post("/accounts/{id}/folders/{folderID}/toggle", s.folderToggle)
		r.Post("/accounts/{id}/folders/{folderID}/policy", s.folderPolicyUpdate)
		r.Post("/accounts/{id}/folders/{folderID}/resync", s.folderResync)
		r.Post("/accounts/{id}/backup", s.backupRun)
		r.Get("/backups", s.backupsList)
		r.Get("/backups/{id}", s.backupDetail)
		r.Get("/browse", s.browseHandler)
		r.Get("/browse/{folderID}", s.browseHandler)
		r.Get("/search", s.searchHandler)
		r.Get("/message/{hash}", s.messageHandler)
		r.Post("/message/{hash}/reprocess", s.messageReprocessHandler)
		r.Get("/attachment/{id}", s.attachmentDownloadHandler)
		r.Post("/export", s.exportHandler)
		r.Get("/oauth/google/start", s.oauthGoogleStart)
		r.Get("/oauth/google/callback", s.oauthGoogleCallback)
	})

	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login.html", map[string]any{"Title": "Log in", "Error": ""})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	email := r.FormValue("email")
	password := r.FormValue("password")

	u, err := s.users.GetByEmail(email)
	if err != nil {
		s.render(w, r, "login.html", map[string]any{"Title": "Log in", "Error": "Invalid email or password."})
		return
	}

	if err := auth.VerifyPassword(u.PasswordHash, password); err != nil {
		s.render(w, r, "login.html", map[string]any{"Title": "Log in", "Error": "Invalid email or password."})
		return
	}

	sess, err := s.sessions.Create(u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	auth.SetSessionCookie(w, sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("mnemosyne_session"); err == nil {
		if id, err := hex.DecodeString(cookie.Value); err == nil {
			_ = s.sessions.Delete(id)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "mnemosyne_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	data := map[string]any{"Title": "Home"}
	if s.accounts != nil {
		accts, _ := s.accounts.List(userID)
		data["Accounts"] = accts
	}
	s.render(w, r, "home.html", data)
}

var navActiveMap = map[string]string{
	"home.html":          "home",
	"browse.html":        "browse",
	"search.html":        "search",
	"message.html":       "search",
	"accounts.html":      "accounts",
	"folders.html":       "accounts",
	"backups.html":       "backups",
	"backup_result.html": "backups",
	"backup_detail.html": "backups",
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	tmpl, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if _, exists := data["NavActive"]; !exists {
		data["NavActive"] = navActiveMap[name]
	}
	if _, exists := data["Email"]; !exists {
		if userID := auth.UserIDFromContext(r.Context()); userID != 0 {
			if u, err := s.users.GetByID(userID); err == nil {
				data["Email"] = u.Email
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
