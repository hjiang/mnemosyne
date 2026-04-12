package httpserver

import (
	"fmt"
	"net/http"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/export"
	"github.com/hjiang/mnemosyne/internal/search"
)

func (s *Server) exportHandler(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	format := r.URL.Query().Get("format")
	queryStr := r.URL.Query().Get("q")

	if format == "" {
		format = "mbox"
	}

	if queryStr == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}

	q, err := search.Parse(queryStr)
	if err != nil {
		http.Error(w, "invalid query: "+err.Error(), http.StatusBadRequest)
		return
	}

	results, err := s.search.Search(q, userID)
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}

	if len(results) == 0 {
		http.Error(w, "no messages match the query", http.StatusNotFound)
		return
	}

	sel := export.NewSelection(results, s.blobs)
	msgs, err := sel.Messages()
	if err != nil {
		http.Error(w, "loading messages: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer export.CloseAll(msgs)

	switch format {
	case "mbox":
		w.Header().Set("Content-Type", "application/mbox")
		w.Header().Set("Content-Disposition", `attachment; filename="export.mbox"`)
		if err := export.WriteMbox(w, msgs); err != nil {
			// Headers already sent; can't change status code.
			return
		}
	case "maildir":
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", `attachment; filename="export.tar"`)
		if err := export.WriteMaildir(w, msgs); err != nil {
			return
		}
	case "imap":
		host := r.URL.Query().Get("host")
		user := r.URL.Query().Get("user")
		pass := r.URL.Query().Get("pass")
		folder := r.URL.Query().Get("folder")
		if host == "" || user == "" || pass == "" || folder == "" {
			http.Error(w, "imap export requires host, user, pass, folder params", http.StatusBadRequest)
			return
		}
		result := export.UploadToIMAP(host, user, pass, folder, false, msgs)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "Uploaded %d messages.\n", result.Uploaded) //nolint:gosec // integer output only, no user input
		for _, e := range result.Errors {
			_, _ = fmt.Fprintf(w, "Error: %v\n", e) //nolint:gosec // error messages, not user-controlled
		}
	default:
		http.Error(w, "unknown format: "+format, http.StatusBadRequest)
	}
}
