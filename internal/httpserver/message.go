package httpserver

import (
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/messages"
)

func (s *Server) messageHandler(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	hashHex := chi.URLParam(r, "hash")

	hash, err := hex.DecodeString(hashHex)
	if err != nil {
		http.Error(w, "invalid message hash", http.StatusBadRequest)
		return
	}

	msg, err := s.messages.GetByHash(hash, userID)
	if err != nil {
		if errors.Is(err, messages.ErrNotFound) {
			http.Error(w, "message not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var dateStr string
	if msg.Date != nil {
		dateStr = time.Unix(*msg.Date, 0).UTC().Format("Mon, 02 Jan 2006 15:04 MST")
	}

	atts, err := s.messages.ListAttachments(msg.Hash)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.render(w, "message.html", map[string]any{
		"Title":       msg.Subject,
		"Message":     msg,
		"DateStr":     dateStr,
		"Attachments": atts,
	})
}
