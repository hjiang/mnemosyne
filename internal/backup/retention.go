package backup

import (
	"fmt"
	"time"

	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/backup/policy"
)

// ApplyRetention runs the retention policy on a folder after a successful backup.
// If backupOK is false, retention is skipped entirely — we never delete upstream
// data unless we've confirmed our local copy is durable.
func ApplyRetention(
	client *imapwrap.Client,
	policyJSON string,
	msgs []policy.Message,
	backupOK bool,
	now time.Time,
) error {
	if !backupOK {
		return nil
	}

	cfg, err := policy.ParseConfig(policyJSON)
	if err != nil {
		return fmt.Errorf("parsing policy: %w", err)
	}

	uids := policy.Apply(cfg, msgs, now)
	if len(uids) == 0 {
		return nil
	}

	if err := client.MarkDeleted(uids); err != nil {
		return fmt.Errorf("marking deleted: %w", err)
	}

	if err := client.Expunge(); err != nil {
		return fmt.Errorf("expunging: %w", err)
	}

	return nil
}
