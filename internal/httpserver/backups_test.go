package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hjiang/mnemosyne/internal/scheduler"
)

func TestBackups_Unauthenticated_Redirects(t *testing.T) {
	env := newAcctTestEnv(t)

	rr := env.doRequest(t, "GET", "/backups", "", nil)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestBackups_ShowsJobs(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "Work Gmail", "imap.gmail.com", 993, "u", "p", true)
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(scheduler.BackupPayload{
		AccountID: acct.ID,
		UserID:    env.userAID,
	})
	if _, err := env.server.queue.Enqueue("backup", string(payload)); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Work Gmail") {
		t.Error("expected account label in response")
	}
	if !strings.Contains(body, "pending") {
		t.Error("expected 'pending' status in response")
	}
}

func TestBackups_UserIsolation(t *testing.T) {
	env := newAcctTestEnv(t)

	acctA, _ := env.accounts.Create(env.userAID, "Alice Mail", "host", 993, "u", "p", true)
	acctB, _ := env.accounts.Create(env.userBID, "Bob Mail", "host", 993, "u", "p", true)

	payloadA, _ := json.Marshal(scheduler.BackupPayload{AccountID: acctA.ID, UserID: env.userAID})
	payloadB, _ := json.Marshal(scheduler.BackupPayload{AccountID: acctB.ID, UserID: env.userBID})
	if _, err := env.server.queue.Enqueue("backup", string(payloadA)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.server.queue.Enqueue("backup", string(payloadB)); err != nil {
		t.Fatal(err)
	}

	// User A should see only their job.
	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	body := rr.Body.String()
	if !strings.Contains(body, "Alice Mail") {
		t.Error("expected Alice's account label")
	}
	if strings.Contains(body, "Bob Mail") {
		t.Error("should not see Bob's account label")
	}
}

func TestBackups_FailedJobShowsError(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true)
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: acct.ID, UserID: env.userAID})
	j, err := env.server.queue.Enqueue("backup", string(payload))
	if err != nil {
		t.Fatal(err)
	}

	// Claim and fail the job.
	if _, err := env.server.queue.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := env.server.queue.Fail(j.ID, "IMAP connection refused"); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	body := rr.Body.String()
	if !strings.Contains(body, "failed") {
		t.Error("expected 'failed' status")
	}
	if !strings.Contains(body, "IMAP connection refused") {
		t.Errorf("expected error message in body, got: %s", body)
	}
}

func TestBackups_EmptyState(t *testing.T) {
	env := newAcctTestEnv(t)

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "No backup jobs yet") {
		t.Error("expected empty state message")
	}
}

func TestBackups_DeletedAccountLabel(t *testing.T) {
	env := newAcctTestEnv(t)

	// Enqueue a job referencing a non-existent account ID.
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: 9999, UserID: env.userAID})
	if _, err := env.server.queue.Enqueue("backup", string(payload)); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	body := rr.Body.String()
	if !strings.Contains(body, "(deleted account)") {
		t.Errorf("expected '(deleted account)' label, got: %s", body)
	}
}
