package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/auth"
)

const browsePageSize = 50

type browseFolder struct {
	ID           int64
	Name         string
	MessageCount int
	Active       bool
}

type browseAccount struct {
	Label   string
	Folders []browseFolder
}

type browseMessage struct {
	Hash    []byte
	Subject string
	From    string
	DateStr string
}

func (s *Server) browseHandler(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())

	// Parse optional folderID from URL.
	var folderID int64
	if idStr := chi.URLParam(r, "folderID"); idStr != "" {
		var err error
		folderID, err = strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}

	// Parse page number.
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}

	// Build sidebar: accounts with nested folders and message counts.
	var sidebar []browseAccount
	if s.accounts != nil {
		accts, _ := s.accounts.List(userID)
		counts, _ := s.messages.CountByFoldersForUser(userID)

		for _, acct := range accts {
			folders, _ := s.accounts.ListFolders(acct.ID)
			ba := browseAccount{Label: acct.Label}
			for _, f := range folders {
				ba.Folders = append(ba.Folders, browseFolder{
					ID:           f.ID,
					Name:         f.Name,
					MessageCount: counts[f.ID],
					Active:       f.ID == folderID,
				})
			}
			sidebar = append(sidebar, ba)
		}
	}

	data := map[string]any{
		"Title":    "Browse",
		"Sidebar":  sidebar,
		"FolderID": folderID,
	}

	// Load messages for selected folder.
	if folderID > 0 {
		// Verify folder ownership.
		folder, err := s.accounts.GetFolderByID(folderID, userID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data["FolderName"] = folder.Name

		total, err := s.messages.CountByFolder(folderID, userID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		totalPages := (total + browsePageSize - 1) / browsePageSize
		if totalPages == 0 {
			totalPages = 1
		}
		if page > totalPages {
			page = totalPages
		}

		offset := (page - 1) * browsePageSize
		msgs, err := s.messages.ListByFolderPaged(folderID, userID, browsePageSize, offset)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		var views []browseMessage
		for _, m := range msgs {
			var dateStr string
			if m.Date != nil {
				dateStr = time.Unix(*m.Date, 0).UTC().Format("Jan 2, 2006")
			}
			subject := m.Subject
			if subject == "" {
				subject = "(no subject)"
			}
			views = append(views, browseMessage{
				Hash:    m.Hash,
				Subject: subject,
				From:    m.FromAddr,
				DateStr: dateStr,
			})
		}

		data["Messages"] = views
		data["TotalCount"] = total
		data["Page"] = page
		data["TotalPages"] = totalPages
		data["PrevPage"] = page - 1
		data["NextPage"] = page + 1
	}

	// HTMX partial: only render the message list fragment.
	if r.Header.Get("HX-Request") == "true" {
		tmpl, ok := s.templates["browse_messages.html"]
		if !ok {
			http.Error(w, "template not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			http.Error(w, fmt.Sprintf("template error: %v", err), http.StatusInternalServerError)
		}
		return
	}

	s.render(w, r, "browse.html", data)
}
