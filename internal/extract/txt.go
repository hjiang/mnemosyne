package extract

import (
	"io"
)

// TextExtractor extracts text from plain text documents.
type TextExtractor struct{}

// Extract reads the entire content as UTF-8 text.
func (e *TextExtractor) Extract(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
