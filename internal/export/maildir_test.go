package export

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

// Test 5: Tar contains new/, cur/, tmp/ directories.
func TestMaildir_Directories(t *testing.T) {
	var buf bytes.Buffer
	msg := Message{
		Hash:         []byte{0xab, 0xcd},
		InternalDate: testDate,
		Body:         strings.NewReader("Subject: Test\r\n\r\nBody\r\n"),
	}
	if err := WriteMaildir(&buf, []Message{msg}); err != nil {
		t.Fatal(err)
	}

	dirs := map[string]bool{}
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeDir {
			dirs[hdr.Name] = true
		}
	}

	for _, d := range []string{"new/", "cur/", "tmp/"} {
		if !dirs[d] {
			t.Errorf("missing directory %q", d)
		}
	}
}

// Test 6: Message is under cur/ with proper filename.
func TestMaildir_MessageFilename(t *testing.T) {
	var buf bytes.Buffer
	msg := Message{
		Hash:         []byte{0xab, 0xcd},
		InternalDate: testDate,
		Flags:        `\Seen \Flagged`,
		Body:         strings.NewReader("Subject: Test\r\n\r\nBody\r\n"),
	}
	if err := WriteMaildir(&buf, []Message{msg}); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	var found bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg && strings.HasPrefix(hdr.Name, "cur/") {
			found = true
			name := hdr.Name[len("cur/"):]
			// Should contain hash hex.
			if !strings.Contains(name, "abcd") {
				t.Errorf("filename %q doesn't contain hash hex", name)
			}
			// Should end with :2,FS (Flagged + Seen, alphabetical).
			if !strings.HasSuffix(name, ":2,FS") {
				t.Errorf("filename %q doesn't end with :2,FS", name)
			}
		}
	}
	if !found {
		t.Error("no file found in cur/")
	}
}

// Test 7: Flag translation.
func TestTranslateFlags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\Seen`, "S"},
		{`\Flagged`, "F"},
		{`\Answered`, "R"},
		{`\Deleted`, "T"},
		{`\Seen \Flagged \Answered`, "FRS"},
		{``, ""},
	}
	for _, tt := range tests {
		got := translateFlags(tt.input)
		if got != tt.want {
			t.Errorf("translateFlags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// Test 8: Tar stream is valid — round-trip.
func TestMaildir_ValidTar(t *testing.T) {
	var buf bytes.Buffer
	msgs := []Message{
		{Hash: []byte{1}, InternalDate: testDate, Body: strings.NewReader("msg1")},
		{Hash: []byte{2}, InternalDate: testDate, Body: strings.NewReader("msg2")},
	}
	if err := WriteMaildir(&buf, msgs); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	count := 0
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid tar at entry %d: %v", count, err)
		}
		count++
	}
	// 3 dirs + 2 files = 5 entries.
	if count != 5 {
		t.Errorf("tar entries = %d, want 5", count)
	}
}
