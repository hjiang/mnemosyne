package imap

import (
	"testing"

	"github.com/hjiang/mnemosyne/internal/testimap"
)

func TestFetchEnvelopes(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)
	srv.SeedMessages(t, "INBOX", 3)

	c, err := Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	sel, err := c.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if sel.NumMessages != 3 {
		t.Fatalf("NumMessages = %d, want 3", sel.NumMessages)
	}

	envs, err := c.FetchEnvelopes(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 3 {
		t.Fatalf("len(envs) = %d, want 3", len(envs))
	}

	// Envelopes are returned in UID order.
	for i, env := range envs {
		wantUID := uint32(i + 1)
		if env.UID != wantUID {
			t.Errorf("envs[%d].UID = %d, want %d", i, env.UID, wantUID)
		}
		if env.Subject == "" {
			t.Errorf("envs[%d].Subject is empty", i)
		}
		if env.MessageID == "" {
			t.Errorf("envs[%d].MessageID is empty", i)
		}
	}
}

func TestFetchBody(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)

	raw := []byte("From: test@example.com\r\nTo: rcpt@example.com\r\nSubject: body test\r\nMessage-ID: <bodytest@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nHello, world!\r\n")
	srv.AppendMessage(t, "INBOX", raw)

	c, err := Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	if _, err := c.SelectFolder("INBOX"); err != nil {
		t.Fatal(err)
	}

	body, err := c.FetchBody(1)
	if err != nil {
		t.Fatal(err)
	}

	if len(body) == 0 {
		t.Fatal("body is empty")
	}

	// The fetched body should contain the message content.
	if got := string(body); got != string(raw) {
		t.Errorf("body mismatch:\ngot:  %q\nwant: %q", got, string(raw))
	}
}

func TestFetchEnvelopes_RFC2047Subject(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)

	// Subject is "你好！" encoded as GB2312 + Base64 per RFC 2047.
	raw := []byte("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: =?GB2312?B?xOO6w6Oh?=\r\nMessage-ID: <rfc2047@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody\r\n")
	srv.AppendMessage(t, "INBOX", raw)

	c, err := Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	if _, err := c.SelectFolder("INBOX"); err != nil {
		t.Fatal(err)
	}

	envs, err := c.FetchEnvelopes(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Fatalf("len(envs) = %d, want 1", len(envs))
	}

	want := "你好！"
	if envs[0].Subject != want {
		t.Errorf("Subject = %q, want %q", envs[0].Subject, want)
	}
}

func TestSelectFolder_Nonexistent(t *testing.T) {
	srv := testimap.New(t)

	c, err := Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	_, err = c.SelectFolder("NONEXISTENT")
	if err == nil {
		t.Fatal("expected error for nonexistent folder")
	}
}

func TestListFolders(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)
	srv.AddFolder(t, "Archive", 1)

	c, err := Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	folders, err := c.ListFolders()
	if err != nil {
		t.Fatal(err)
	}

	if len(folders) < 2 {
		t.Fatalf("len(folders) = %d, want >= 2", len(folders))
	}

	names := make(map[string]bool)
	for _, f := range folders {
		names[f] = true
	}
	if !names["INBOX"] {
		t.Error("missing INBOX in folder list")
	}
	if !names["Archive"] {
		t.Error("missing Archive in folder list")
	}
}

func TestSelectFolder_UIDValidity(t *testing.T) {
	srv := testimap.New(t)
	srv.AddFolder(t, "INBOX", 1)

	c, err := Dial(srv.Addr, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	sel, err := c.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}

	if sel.UIDValidity == 0 {
		t.Error("UIDValidity should be non-zero")
	}
}
