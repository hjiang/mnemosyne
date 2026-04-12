// Package extract provides text extraction from various document formats.
package extract

import (
	"io"
)

// Extractor extracts text content from a document.
type Extractor interface {
	Extract(r io.Reader) (string, error)
}

// Registry maps MIME types to extractors.
type Registry struct {
	extractors map[string]Extractor
	noop       Extractor
}

// NewRegistry creates a Registry with default extractors registered.
func NewRegistry() *Registry {
	r := &Registry{
		extractors: make(map[string]Extractor),
		noop:       &noopExtractor{},
	}
	r.Register("text/plain", &TextExtractor{})
	r.Register("text/html", &HTMLExtractor{})
	r.Register("application/pdf", &PDFExtractor{})
	r.Register("application/vnd.openxmlformats-officedocument.wordprocessingml.document", &DocxExtractor{})
	return r
}

// Register adds an extractor for a MIME type.
func (r *Registry) Register(mime string, e Extractor) {
	r.extractors[mime] = e
}

// For returns the extractor for a MIME type, or a no-op extractor if unknown.
func (r *Registry) For(mime string) Extractor {
	if e, ok := r.extractors[mime]; ok {
		return e
	}
	return r.noop
}

type noopExtractor struct{}

func (n *noopExtractor) Extract(_ io.Reader) (string, error) {
	return "", nil
}
