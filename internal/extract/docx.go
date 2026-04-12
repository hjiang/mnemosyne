package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// DocxExtractor extracts text from OOXML (.docx) documents.
type DocxExtractor struct{}

// Extract reads a docx file and returns the text content from word/document.xml.
func (e *DocxExtractor) Extract(r io.Reader) (string, error) {
	// Read all bytes since zip.NewReader needs a ReaderAt + size.
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("reading docx: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("opening zip: %w", err)
	}

	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("opening document.xml: %w", err)
			}
			defer rc.Close() //nolint:errcheck
			return parseDocumentXML(rc)
		}
	}

	return "", fmt.Errorf("word/document.xml not found in docx")
}

// parseDocumentXML extracts text from <w:t> elements.
func parseDocumentXML(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var b strings.Builder
	inText := false

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parsing XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
			if t.Name.Local == "p" {
				b.WriteByte(' ')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}

	return strings.TrimSpace(b.String()), nil
}
