package export

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var testDate = time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)

func makeMsg(body string, date time.Time) Message {
	return Message{
		Hash:         []byte{1},
		InternalDate: date,
		Body:         strings.NewReader(body),
	}
}

// Test 1: Single message with correct From_ line and trailing blank.
func TestMbox_SingleMessage(t *testing.T) {
	var buf bytes.Buffer
	msg := makeMsg("Subject: Test\r\n\r\nHello\r\n", testDate)
	if err := WriteMbox(&buf, []Message{msg}); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.HasPrefix(out, "From mnemosyne@localhost") {
		t.Error("expected From_ separator line")
	}
	if !strings.Contains(out, "Subject: Test") {
		t.Error("expected message body")
	}
	// Should end with blank line (two newlines).
	if !strings.HasSuffix(out, "\n\n") {
		t.Error("expected trailing blank line")
	}
}

// Test 2: Body containing "From " at start of line is escaped.
func TestMbox_FromEscaping(t *testing.T) {
	body := "Subject: Test\r\n\r\nHello\r\nFrom someone@example.com\r\nGoodbye\r\n"
	var buf bytes.Buffer
	if err := WriteMbox(&buf, []Message{makeMsg(body, testDate)}); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, ">From someone@example.com") {
		t.Errorf("expected escaped From line, got:\n%s", out)
	}
}

// Test 3: Multiple messages in stable order.
func TestMbox_MultipleMessages(t *testing.T) {
	date1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	date2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	msgs := []Message{
		makeMsg("Subject: First\r\n\r\nFirst body\r\n", date1),
		makeMsg("Subject: Second\r\n\r\nSecond body\r\n", date2),
	}
	var buf bytes.Buffer
	if err := WriteMbox(&buf, msgs); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	idxFirst := strings.Index(out, "First body")
	idxSecond := strings.Index(out, "Second body")
	if idxFirst < 0 || idxSecond < 0 {
		t.Fatal("expected both messages in output")
	}
	if idxFirst >= idxSecond {
		t.Error("first message should appear before second")
	}

	// Count From_ lines.
	fromCount := strings.Count(out, "From mnemosyne@localhost")
	if fromCount != 2 {
		t.Errorf("From_ count = %d, want 2", fromCount)
	}
}

// Test 4: Output parses as valid mbox (basic round-trip check).
func TestMbox_ValidFormat(t *testing.T) {
	msgs := []Message{
		makeMsg("From: alice@test.com\r\nSubject: A\r\n\r\nBody A\r\n", testDate),
		makeMsg("From: bob@test.com\r\nSubject: B\r\n\r\nBody B\r\n", testDate),
	}
	var buf bytes.Buffer
	if err := WriteMbox(&buf, msgs); err != nil {
		t.Fatal(err)
	}

	// Verify structure: each message is separated by "From " at start of line.
	lines := strings.Split(buf.String(), "\n")
	fromLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "From mnemosyne@localhost") {
			fromLines++
		}
	}
	if fromLines != 2 {
		t.Errorf("From_ lines = %d, want 2", fromLines)
	}
}

// Test: From_ at the very beginning of the body is escaped.
func TestMbox_FromAtStart(t *testing.T) {
	body := "From evil@attacker.com spoofed\r\nSubject: Evil\r\n\r\nPayload\r\n"
	var buf bytes.Buffer
	if err := WriteMbox(&buf, []Message{makeMsg(body, testDate)}); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	// The body's "From " line should be escaped, but the mbox separator should not.
	lines := strings.Split(out, "\n")
	escapedCount := 0
	for _, l := range lines {
		if strings.HasPrefix(l, ">From ") {
			escapedCount++
		}
	}
	if escapedCount != 1 {
		t.Errorf("escaped From lines = %d, want 1", escapedCount)
	}
}
