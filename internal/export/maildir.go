package export

import (
	"archive/tar"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// WriteMaildir writes messages as a tar stream containing a Maildir layout.
func WriteMaildir(w io.Writer, messages []Message) error {
	tw := tar.NewWriter(w)
	defer tw.Close() //nolint:errcheck

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	// Create Maildir directories.
	for _, dir := range []string{"new/", "cur/", "tmp/"} {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir,
			Name:     dir,
			Mode:     0o750,
			ModTime:  time.Now(),
		}); err != nil {
			return fmt.Errorf("writing dir header: %w", err)
		}
	}

	for _, msg := range messages {
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			return fmt.Errorf("reading message body: %w", err)
		}

		filename := maildirFilename(msg, hostname)
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "cur/" + filename,
			Size:     int64(len(body)),
			Mode:     0o640,
			ModTime:  msg.InternalDate,
		}); err != nil {
			return fmt.Errorf("writing file header: %w", err)
		}
		if _, err := tw.Write(body); err != nil {
			return fmt.Errorf("writing file body: %w", err)
		}
	}

	return tw.Close()
}

func maildirFilename(msg Message, hostname string) string {
	ts := msg.InternalDate.Unix()
	hashHex := hex.EncodeToString(msg.Hash)
	flags := translateFlags(msg.Flags)
	return fmt.Sprintf("%d.%s.%s:2,%s", ts, hashHex, hostname, flags)
}

// translateFlags converts IMAP flag strings to Maildir info suffixes.
// Flags must be sorted alphabetically per Maildir spec.
func translateFlags(imapFlags string) string {
	var flags []string
	lower := strings.ToLower(imapFlags)
	if strings.Contains(lower, `\flagged`) {
		flags = append(flags, "F")
	}
	if strings.Contains(lower, `\answered`) {
		flags = append(flags, "R")
	}
	if strings.Contains(lower, `\seen`) {
		flags = append(flags, "S")
	}
	if strings.Contains(lower, `\deleted`) {
		flags = append(flags, "T")
	}
	sort.Strings(flags)
	return strings.Join(flags, "")
}
