package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hjiang/mnemosyne/internal/backup"
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

	acct, err := env.accounts.Create(env.userAID, "Work Gmail", "imap.gmail.com", 993, "u", "p", true, "", 0, "", "")
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

	acctA, _ := env.accounts.Create(env.userAID, "Alice Mail", "host", 993, "u", "p", true, "", 0, "", "")
	acctB, _ := env.accounts.Create(env.userBID, "Bob Mail", "host", 993, "u", "p", true, "", 0, "", "")

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

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
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

func TestBackupDetail_ShowsErrors(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: acct.ID, UserID: env.userAID})
	j, err := env.server.queue.Enqueue("backup", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.server.queue.Claim(); err != nil {
		t.Fatal(err)
	}
	errMsg := "folder INBOX: timeout\nfolder Sent: timeout"
	if err := env.server.queue.Fail(j.ID, errMsg); err != nil {
		t.Fatal(err)
	}

	path := "/backups/" + strconv.FormatInt(j.ID, 10)
	rr := env.doRequest(t, "GET", path, env.cookieA, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "folder INBOX: timeout") {
		t.Error("expected first error line in body")
	}
	if !strings.Contains(body, "folder Sent: timeout") {
		t.Error("expected second error line in body")
	}
}

func TestBackupDetail_UserIsolation(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: acct.ID, UserID: env.userAID})
	j, err := env.server.queue.Enqueue("backup", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.server.queue.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := env.server.queue.Fail(j.ID, "some error"); err != nil {
		t.Fatal(err)
	}

	path := "/backups/" + strconv.FormatInt(j.ID, 10)
	rr := env.doRequest(t, "GET", path, env.cookieB, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (user B should not see user A's job)", rr.Code, http.StatusNotFound)
	}
}

func TestBackupDetail_NotFound(t *testing.T) {
	env := newAcctTestEnv(t)

	rr := env.doRequest(t, "GET", "/backups/99999", env.cookieA, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestBackups_DoneJobShowsSummary(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: acct.ID, UserID: env.userAID})
	j, err := env.server.queue.Enqueue("backup", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.server.queue.Claim(); err != nil {
		t.Fatal(err)
	}
	prog, _ := json.Marshal(backup.Progress{Done: true, NewMessages: 42})
	if err := env.server.queue.UpdateProgress(j.ID, string(prog)); err != nil {
		t.Fatal(err)
	}
	if err := env.server.queue.Complete(j.ID); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	body := rr.Body.String()
	if !strings.Contains(body, "42 emails fetched") {
		t.Errorf("expected summary with message count, got: %s", body)
	}
}

func TestBackups_FailedJobShowsSummaryWithErrors(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: acct.ID, UserID: env.userAID})
	j, err := env.server.queue.Enqueue("backup", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.server.queue.Claim(); err != nil {
		t.Fatal(err)
	}
	prog, _ := json.Marshal(backup.Progress{Done: true, NewMessages: 10, ErrorCount: 2})
	if err := env.server.queue.UpdateProgress(j.ID, string(prog)); err != nil {
		t.Fatal(err)
	}
	if err := env.server.queue.Fail(j.ID, "folder INBOX: timeout\nfolder Sent: timeout"); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	body := rr.Body.String()
	if !strings.Contains(body, "10 emails fetched") {
		t.Errorf("expected message count in summary, got: %s", body)
	}
	if !strings.Contains(body, "2 errors") {
		t.Errorf("expected error count in summary, got: %s", body)
	}
}

func TestBackupsList_FailedJobLinksToDetail(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	payload, _ := json.Marshal(scheduler.BackupPayload{AccountID: acct.ID, UserID: env.userAID})
	j, err := env.server.queue.Enqueue("backup", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.server.queue.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := env.server.queue.Fail(j.ID, "connection refused"); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", "/backups", env.cookieA, nil)
	body := rr.Body.String()
	wantLink := `href="/backups/` + strconv.FormatInt(j.ID, 10)
	if !strings.Contains(body, wantLink) {
		t.Errorf("expected link %q in body", wantLink)
	}
}
