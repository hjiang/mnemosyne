// Package testimap provides an in-memory IMAP server for testing.
package testimap

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Server is a test IMAP server backed by an in-memory store.
type Server struct {
	memServer *imapmemserver.Server
	imapSrv   *imapserver.Server
	listener  net.Listener
	user      *imapmemserver.User
	Addr      string
	Username  string
	Password  string
}

// New creates and starts a test IMAP server on a random port.
// The server is stopped when the test ends.
func New(t *testing.T) *Server {
	t.Helper()

	mem := imapmemserver.New()
	user := imapmemserver.NewUser("testuser", "testpass")
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return imapmemserver.NewUserSession(user), nil, nil
		},
		InsecureAuth: true,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !strings.Contains(err.Error(), "closed") {
			_ = err // Server shut down, ignore.
		}
	}()

	t.Cleanup(func() {
		_ = srv.Close()
	})

	return &Server{
		memServer: mem,
		imapSrv:   srv,
		listener:  ln,
		user:      user,
		Addr:      ln.Addr().String(),
		Username:  "testuser",
		Password:  "testpass",
	}
}

// AddFolder creates a mailbox with the given name and UIDVALIDITY.
func (s *Server) AddFolder(t *testing.T, name string, _ uint32) {
	t.Helper()
	err := s.user.Create(name, nil)
	if err != nil {
		t.Fatalf("creating folder %q: %v", name, err)
	}
}

// AppendMessage adds a raw RFC822 message to a folder.
func (s *Server) AppendMessage(t *testing.T, folder string, body []byte) {
	t.Helper()
	literal := newLiteralReader(body)
	opts := &imap.AppendOptions{
		Flags: []imap.Flag{},
		Time:  time.Now(),
	}
	_, err := s.user.Append(folder, literal, opts)
	if err != nil {
		t.Fatalf("appending to %q: %v", folder, err)
	}
}

// SeedMessages adds n numbered test messages to the given folder.
func (s *Server) SeedMessages(t *testing.T, folder string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		body := fmt.Sprintf("From: sender%d@test.com\r\nTo: rcpt@test.com\r\nSubject: Test message %d\r\nMessage-ID: <msg%d@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody of message %d\r\n", i, i, i, i)
		s.AppendMessage(t, folder, []byte(body))
	}
}

// SeedMessageWithBody adds a message with a specific body to a folder.
func (s *Server) SeedMessageWithBody(t *testing.T, folder string, _ string, body []byte) {
	t.Helper()
	s.AppendMessage(t, folder, body)
}

// literalReader implements imap.LiteralReader (io.Reader + Size).
type literalReader struct {
	data []byte
	pos  int
}

func newLiteralReader(data []byte) *literalReader {
	return &literalReader{data: data}
}

func (r *literalReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, io.EOF
	}
	return n, nil
}

func (r *literalReader) Size() int64 {
	return int64(len(r.data))
}
