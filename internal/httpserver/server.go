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
	search    *search.Executor
	blobs     *blobs.Store
}

// New creates an HTTP server with all routes wired.
// acctRepo and orch may be nil if IMAP features are not yet configured.
func New(userRepo *users.Repo, sessions *auth.SessionStore, acctRepo *accounts.Repo, orch *backup.Orchestrator, jobQueue *jobs.Queue, searchExec *search.Executor, blobStore *blobs.Store) *Server {
	templates := map[string]*template.Template{
		"login.html":        template.Must(template.ParseFS(templateFS, "templates/login.html")),
		"home.html":         template.Must(template.ParseFS(templateFS, "templates/home.html")),
		"accounts.html":     template.Must(template.ParseFS(templateFS, "templates/accounts.html")),
		"folders.html":      template.Must(template.ParseFS(templateFS, "templates/folders.html")),
		"backup_result.html": template.Must(template.ParseFS(templateFS, "templates/backup_result.html")),
		"search.html":        template.Must(template.ParseFS(templateFS, "templates/search.html")),
	}

	s := &Server{
		router:    chi.NewRouter(),
		templates: templates,
		users:     userRepo,
		sessions:  sessions,
		accounts:  acctRepo,
		backup:    orch,
		queue:     jobQueue,
		search:    searchExec,
		blobs:     blobStore,
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
		r.Get("/accounts/{id}/folders", s.accountFolders)
		r.Post("/accounts/{id}/folders/{folderID}/toggle", s.folderToggle)
		r.Post("/accounts/{id}/folders/{folderID}/policy", s.folderPolicyUpdate)
		r.Post("/accounts/{id}/backup", s.backupRun)
		r.Get("/search", s.searchHandler)
		r.Post("/export", s.exportHandler)
	})

	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) loginForm(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "login.html", map[string]any{"Title": "Log in", "Error": ""})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	email := r.FormValue("email")
	password := r.FormValue("password")

	u, err := s.users.GetByEmail(email)
	if err != nil {
		s.render(w, "login.html", map[string]any{"Title": "Log in", "Error": "Invalid email or password."})
		return
	}

	if err := auth.VerifyPassword(u.PasswordHash, password); err != nil {
		s.render(w, "login.html", map[string]any{"Title": "Log in", "Error": "Invalid email or password."})
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
	u, err := s.users.GetByID(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := map[string]any{"Title": "Home", "Email": u.Email}
	if s.accounts != nil {
		accts, _ := s.accounts.List(userID)
		data["Accounts"] = accts
	}
	s.render(w, "home.html", data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
