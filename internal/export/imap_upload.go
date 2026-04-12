package export

import (
	"fmt"
	"io"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// UploadResult tracks per-message upload results.
type UploadResult struct {
	Uploaded int
	Errors   []error
}

// UploadToIMAP connects to a target IMAP server and APPENDs each message
// to the specified folder. The connection is one-shot and not stored.
func UploadToIMAP(
	addr, username, password, folder string,
	useTLS bool,
	messages []Message,
) *UploadResult {
	result := &UploadResult{}

	var client *imapclient.Client
	var err error
	if useTLS {
		client, err = imapclient.DialTLS(addr, nil)
	} else {
		client, err = imapclient.DialInsecure(addr, nil)
	}
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("connecting: %w", err))
		return result
	}
	defer client.Close() //nolint:errcheck

	if err := client.Login(username, password).Wait(); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("login: %w", err))
		return result
	}

	// Create folder if it doesn't exist (ignore error — might already exist).
	_ = client.Create(folder, nil).Wait()

	for _, msg := range messages {
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("reading body: %w", err))
			continue
		}

		opts := &goimap.AppendOptions{
			Time: msg.InternalDate,
		}
		cmd := client.Append(folder, int64(len(body)), opts)
		if _, err := cmd.Write(body); err != nil {
			_ = cmd.Close()
			result.Errors = append(result.Errors, fmt.Errorf("writing message: %w", err))
			continue
		}
		if err := cmd.Close(); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("closing append: %w", err))
			continue
		}
		if _, err := cmd.Wait(); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("append wait: %w", err))
			continue
		}
		result.Uploaded++
	}

	_ = client.Logout().Wait()
	return result
}
