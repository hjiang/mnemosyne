package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/jobs"
	"github.com/hjiang/mnemosyne/internal/scheduler"
)

type backupJobView struct {
	ID           int64
	AccountLabel string
	State        string
	ErrorLines   []string
	CreatedAt    time.Time
	StartedAt    string
	Duration     string
	Attempts     int
	Progress     string // human-readable progress for running jobs
}

func (s *Server) backupsList(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	now := time.Now()

	var views []backupJobView

	if s.queue != nil {
		jobs, err := s.queue.ListByUser(userID, 50)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		views = make([]backupJobView, len(jobs))
		for i, j := range jobs {
			v := backupJobView{
				ID:        j.ID,
				State:     j.State,
				CreatedAt: time.Unix(j.CreatedAt, 0).UTC(),
				Attempts:  j.Attempts,
			}

			if j.Error != "" {
				v.ErrorLines = strings.Split(j.Error, "\n")
			}

			v.AccountLabel = resolveAccountLabel(s, userID, j.Payload)

			if j.StartedAt != nil {
				v.StartedAt = time.Unix(*j.StartedAt, 0).UTC().Format("Jan 2, 2006 3:04 PM")
			}

			if j.StartedAt != nil && j.FinishedAt != nil {
				d := time.Duration(*j.FinishedAt-*j.StartedAt) * time.Second
				v.Duration = d.Truncate(time.Second).String()
			} else if j.StartedAt != nil && j.State == "running" {
				d := now.Sub(time.Unix(*j.StartedAt, 0)).Truncate(time.Second)
				v.Duration = fmt.Sprintf("running for %s", d)
			}

			if j.State == "running" && j.Progress != "" {
				var prog backup.Progress
				if err := json.Unmarshal([]byte(j.Progress), &prog); err == nil {
					v.Progress = fmt.Sprintf("Syncing %s (%d/%d folders, %d messages)",
						prog.Folder, prog.FolderIndex, prog.FolderTotal, prog.NewMessages)
				}
			}

			views[i] = v
		}
	}

	s.render(w, r, "backups.html", map[string]any{
		"Title": "Backup History",
		"Jobs":  views,
	})
}

func (s *Server) backupDetail(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.queue == nil {
		http.NotFound(w, r)
		return
	}
	job, err := s.queue.GetByIDForUser(id, userID)
	if err != nil {
		if errors.Is(err, jobs.ErrJobNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var errorLines []string
	if job.Error != "" {
		errorLines = strings.Split(job.Error, "\n")
	}
	s.render(w, r, "backup_detail.html", map[string]any{
		"Title":        "Backup Job Detail",
		"JobID":        job.ID,
		"AccountLabel": resolveAccountLabel(s, userID, job.Payload),
		"State":        job.State,
		"ErrorLines":   errorLines,
	})
}

// resolveAccountLabel returns the display label for the account referenced in a job payload.
func resolveAccountLabel(s *Server, userID int64, payload string) string {
	if s.accounts == nil {
		return "Unknown"
	}
	var bp scheduler.BackupPayload
	if err := json.Unmarshal([]byte(payload), &bp); err != nil {
		return "Unknown"
	}
	accts, _ := s.accounts.List(userID)
	for _, a := range accts {
		if a.ID == bp.AccountID {
			return a.Label
		}
	}
	return "(deleted account)"
}
