package backup

import (
	"testing"
	"time"

	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/backup/policy"
	"github.com/hjiang/mnemosyne/internal/testimap"
)

var retentionNow = time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)

func connectTestIMAP(t *testing.T, srv *testimap.Server) *imapwrap.Client {
	t.Helper()
	c, err := imapwrap.Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Test 9: Apply expunges messages per policy.
func TestRetention_Expunge(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)
	srv.SeedMessages(t, "INBOX", 5)

	client := connectTestIMAP(t, srv)
	if _, err := client.SelectFolder("INBOX"); err != nil {
		t.Fatal(err)
	}

	// newest_n=2: keep 2 newest, expunge 3 oldest.
	msgs := make([]policy.Message, 5)
	for i := 0; i < 5; i++ {
		msgs[i] = policy.Message{UID: uint32(i + 1), InternalDate: int64(i + 1)}
	}

	err := ApplyRetention(client, `{"leave_on_server":"newest_n","n":2}`, msgs, true, retentionNow)
	if err != nil {
		t.Fatal(err)
	}

	// Verify: re-select and check message count.
	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 2 {
		t.Errorf("NumMessages = %d, want 2 after expunge", info.NumMessages)
	}
}

// Test 10: Apply does nothing when backupOK is false.
func TestRetention_BackupNotOK(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)
	srv.SeedMessages(t, "INBOX", 5)

	client := connectTestIMAP(t, srv)
	if _, err := client.SelectFolder("INBOX"); err != nil {
		t.Fatal(err)
	}

	msgs := make([]policy.Message, 5)
	for i := 0; i < 5; i++ {
		msgs[i] = policy.Message{UID: uint32(i + 1), InternalDate: int64(i + 1)}
	}

	// backupOK=false → nothing should be deleted.
	err := ApplyRetention(client, `{"leave_on_server":"newest_n","n":0}`, msgs, false, retentionNow)
	if err != nil {
		t.Fatal(err)
	}

	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 5 {
		t.Errorf("NumMessages = %d, want 5 (nothing should be expunged)", info.NumMessages)
	}
}

// Test 11: Apply is idempotent — running twice is safe.
func TestRetention_Idempotent(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)
	srv.SeedMessages(t, "INBOX", 5)

	client := connectTestIMAP(t, srv)
	if _, err := client.SelectFolder("INBOX"); err != nil {
		t.Fatal(err)
	}

	msgs := make([]policy.Message, 5)
	for i := 0; i < 5; i++ {
		msgs[i] = policy.Message{UID: uint32(i + 1), InternalDate: int64(i + 1)}
	}

	policyJSON := `{"leave_on_server":"newest_n","n":3}`

	// First apply.
	if err := ApplyRetention(client, policyJSON, msgs, true, retentionNow); err != nil {
		t.Fatal(err)
	}

	// Second apply with same messages — the already-deleted UIDs should not cause errors.
	// Note: the UIDs 1,2 are already gone, so MarkDeleted on them is a no-op.
	if err := ApplyRetention(client, policyJSON, msgs, true, retentionNow); err != nil {
		t.Fatal(err)
	}

	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 3 {
		t.Errorf("NumMessages = %d, want 3", info.NumMessages)
	}
}

// Test: "all" policy with backupOK=true does nothing.
func TestRetention_AllPolicy(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)
	srv.SeedMessages(t, "INBOX", 3)

	client := connectTestIMAP(t, srv)
	if _, err := client.SelectFolder("INBOX"); err != nil {
		t.Fatal(err)
	}

	msgs := make([]policy.Message, 3)
	for i := 0; i < 3; i++ {
		msgs[i] = policy.Message{UID: uint32(i + 1), InternalDate: int64(i + 1)}
	}

	err := ApplyRetention(client, `{"leave_on_server":"all"}`, msgs, true, retentionNow)
	if err != nil {
		t.Fatal(err)
	}

	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 3 {
		t.Errorf("NumMessages = %d, want 3", info.NumMessages)
	}
}
