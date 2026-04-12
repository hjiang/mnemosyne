package extract

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// PDFExtractor extracts text from PDF documents using pdftotext.
type PDFExtractor struct{}

// Extract shells out to pdftotext to extract text from a PDF.
func (e *PDFExtractor) Extract(r io.Reader) (string, error) {
	// Check if pdftotext is available.
	pdftotextPath, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", fmt.Errorf("pdftotext not found: %w", err)
	}

	// Write input to a temp file (pdftotext needs a seekable file).
	tmp, err := os.CreateTemp("", "mnemosyne-pdf-*.pdf")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	_ = tmp.Close()

	// Run pdftotext: input.pdf - (dash means stdout).
	cmd := exec.Command(pdftotextPath, "-enc", "UTF-8", tmp.Name(), "-") //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running pdftotext: %w", err)
	}
	return string(out), nil
}
