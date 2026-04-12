// Package export implements email export in mbox, Maildir, and IMAP upload formats.
package export

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"time"
)

// Message is a single message to export.
type Message struct {
	Hash         []byte
	InternalDate time.Time
	Flags        string
	Body         io.Reader
}

// WriteMbox writes messages in mbox format to w.
// Messages should be provided in the desired order (typically date ascending).
func WriteMbox(w io.Writer, messages []Message) error {
	bw := bufio.NewWriter(w)

	for _, msg := range messages {
		// Write the From_ separator line.
		fromLine := fmt.Sprintf("From mnemosyne@localhost %s\n",
			msg.InternalDate.UTC().Format(time.ANSIC))
		if _, err := bw.WriteString(fromLine); err != nil {
			return fmt.Errorf("writing From line: %w", err)
		}

		// Read the full body and escape From_ lines.
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			return fmt.Errorf("reading message body: %w", err)
		}

		escaped := escapeMboxFrom(body)
		if _, err := bw.Write(escaped); err != nil {
			return fmt.Errorf("writing message body: %w", err)
		}

		// Ensure trailing newline and blank line separator.
		if len(escaped) == 0 || escaped[len(escaped)-1] != '\n' {
			if err := bw.WriteByte('\n'); err != nil {
				return err
			}
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}

	return bw.Flush()
}

// escapeMboxFrom escapes lines starting with "From " per mboxrd convention.
func escapeMboxFrom(body []byte) []byte {
	// Fast path: no escaping needed.
	if !bytes.Contains(body, []byte("\nFrom ")) && !bytes.HasPrefix(body, []byte("From ")) {
		return body
	}

	var buf bytes.Buffer
	buf.Grow(len(body) + 64)

	lines := bytes.Split(body, []byte("\n"))
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("From ")) {
			buf.WriteByte('>')
		}
		buf.Write(line)
		if i < len(lines)-1 {
			buf.WriteByte('\n')
		}
	}

	return buf.Bytes()
}
