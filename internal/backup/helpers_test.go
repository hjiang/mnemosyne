package backup

import (
	"testing"

	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/testimap"
)

func connectTestIMAP(t *testing.T, srv *testimap.Server) *imapwrap.Client {
	t.Helper()
	c, err := imapwrap.Dial(srv.Addr, srv.Username, srv.Password, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
