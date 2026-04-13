package httpserver

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/blobs"
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

	s.render(w, r, "message.html", map[string]any{
		"Title":       msg.Subject,
		"Message":     msg,
		"DateStr":     dateStr,
		"Attachments": atts,
	})
}

func (s *Server) attachmentDownloadHandler(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid attachment id", http.StatusBadRequest)
		return
	}

	att, err := s.messages.GetAttachment(id, userID)
	if err != nil {
		if errors.Is(err, messages.ErrNotFound) {
			http.Error(w, "attachment not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rc, err := s.blobs.Get(att.BlobHash)
	if err != nil {
		if errors.Is(err, blobs.ErrNotFound) {
			http.Error(w, "attachment data not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close() //nolint:errcheck

	filename := att.Filename
	if filename == "" {
		filename = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if att.MimeType != "" {
		w.Header().Set("Content-Type", att.MimeType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if att.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(att.Size, 10))
	}

	io.Copy(w, rc) //nolint:errcheck,gosec
}

func (s *Server) messageReprocessHandler(w http.ResponseWriter, r *http.Request) {
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

	rc, err := s.blobs.Get(msg.Hash)
	if err != nil {
		if errors.Is(err, blobs.ErrNotFound) {
			http.Error(w, "raw message blob not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	raw, err := io.ReadAll(rc)
	rc.Close() //nolint:errcheck,gosec
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	bodyText := backup.ExtractBodyText(raw)
	if err := s.messages.UpdateBodyText(msg.Hash, bodyText); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.messages.ReindexFTS(msg.Hash, msg.Subject, msg.FromAddr, msg.ToAddrs, msg.CcAddrs, bodyText)

	http.Redirect(w, r, "/message/"+hashHex, http.StatusSeeOther)
}
