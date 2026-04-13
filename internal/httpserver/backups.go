package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/scheduler"
)

type backupJobView struct {
	ID           int64
	AccountLabel string
	State        string
	Error        string
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

		// Build account ID → label map.
		labels := make(map[int64]string)
		if s.accounts != nil {
			accts, _ := s.accounts.List(userID)
			for _, a := range accts {
				labels[a.ID] = a.Label
			}
		}

		views = make([]backupJobView, len(jobs))
		for i, j := range jobs {
			v := backupJobView{
				ID:        j.ID,
				State:     j.State,
				Error:     j.Error,
				CreatedAt: time.Unix(j.CreatedAt, 0).UTC(),
				Attempts:  j.Attempts,
			}

			var bp scheduler.BackupPayload
			if err := json.Unmarshal([]byte(j.Payload), &bp); err == nil {
				if label, ok := labels[bp.AccountID]; ok {
					v.AccountLabel = label
				} else {
					v.AccountLabel = "(deleted account)"
				}
			} else {
				v.AccountLabel = "Unknown"
			}

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
