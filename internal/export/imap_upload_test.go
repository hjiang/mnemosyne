package export

import (
	"strings"
	"testing"

	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/testimap"
)

// Test 9: Upload 3 messages via APPEND.
func TestUploadToIMAP_Success(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "Exported", 1)

	msgs := []Message{
		makeMsg("From: a@test.com\r\nSubject: A\r\n\r\nBody A\r\n", testDate),
		makeMsg("From: b@test.com\r\nSubject: B\r\n\r\nBody B\r\n", testDate),
		makeMsg("From: c@test.com\r\nSubject: C\r\n\r\nBody C\r\n", testDate),
	}

	result := UploadToIMAP(srv.Addr, srv.Username, srv.Password, "Exported", false, msgs)
	if result.Uploaded != 3 {
		t.Errorf("Uploaded = %d, want 3", result.Uploaded)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v", result.Errors)
	}

	// Verify messages are on the server.
	c, err := imapwrap.Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	info, err := c.SelectFolder("Exported")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 3 {
		t.Errorf("NumMessages = %d, want 3", info.NumMessages)
	}
}

// Test 10: Target folder is created if it doesn't exist.
func TestUploadToIMAP_CreatesFolder(t *testing.T) {
	srv := testimap.New(t)
	// Don't pre-create the folder.

	msgs := []Message{
		makeMsg("Subject: Test\r\n\r\nBody\r\n", testDate),
	}

	result := UploadToIMAP(srv.Addr, srv.Username, srv.Password, "NewFolder", false, msgs)
	if result.Uploaded != 1 {
		t.Errorf("Uploaded = %d, want 1", result.Uploaded)
	}
}

// Test 11: Auth failure is reported.
func TestUploadToIMAP_AuthFailure(t *testing.T) {
	srv := testimap.New(t)

	msgs := []Message{
		makeMsg("Subject: Test\r\n\r\nBody\r\n", testDate),
	}

	result := UploadToIMAP(srv.Addr, "wrong", "wrong", "INBOX", false, msgs)
	if result.Uploaded != 0 {
		t.Errorf("Uploaded = %d, want 0", result.Uploaded)
	}
	if len(result.Errors) == 0 {
		t.Error("expected auth error")
	}
}

// Test 12: Connection error surfaces gracefully.
func TestUploadToIMAP_ConnectionError(t *testing.T) {
	msgs := []Message{
		makeMsg("Subject: Test\r\n\r\nBody\r\n", testDate),
	}

	result := UploadToIMAP("127.0.0.1:1", "u", "p", "INBOX", false, msgs)
	if result.Uploaded != 0 {
		t.Errorf("Uploaded = %d, want 0", result.Uploaded)
	}
	if len(result.Errors) == 0 {
		t.Error("expected connection error")
	}
	if !strings.Contains(result.Errors[0].Error(), "connecting") {
		t.Errorf("error = %q, expected connecting error", result.Errors[0])
	}
}
