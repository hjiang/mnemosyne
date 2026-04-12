package extract

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// Test 19: txt.Extract returns raw bytes as UTF-8.
func TestTextExtractor(t *testing.T) {
	e := &TextExtractor{}
	text, err := e.Extract(strings.NewReader("Hello, world!"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello, world!" {
		t.Errorf("text = %q, want 'Hello, world!'", text)
	}
}

func TestTextExtractor_Empty(t *testing.T) {
	e := &TextExtractor{}
	text, err := e.Extract(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
}

// Test 20: html.Extract strips tags and excludes script/style.
func TestHTMLExtractor(t *testing.T) {
	htmlDoc := `<html><head><style>body{color:red}</style></head><body>
		<h1>Title</h1>
		<p>Hello <b>world</b></p>
		<script>alert('xss')</script>
		<p>Goodbye</p>
	</body></html>`
	e := &HTMLExtractor{}
	text, err := e.Extract(strings.NewReader(htmlDoc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Title") {
		t.Error("expected 'Title' in output")
	}
	if !strings.Contains(text, "Hello") {
		t.Error("expected 'Hello' in output")
	}
	if !strings.Contains(text, "world") {
		t.Error("expected 'world' in output")
	}
	if !strings.Contains(text, "Goodbye") {
		t.Error("expected 'Goodbye' in output")
	}
	if strings.Contains(text, "alert") {
		t.Error("script content should not be in output")
	}
	if strings.Contains(text, "color:red") {
		t.Error("style content should not be in output")
	}
}

// Test 21: pdf.Extract against a fixture.
func TestPDFExtractor(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not available")
	}

	pdf := minimalPDF("Hello PDF")
	e := &PDFExtractor{}
	text, err := e.Extract(bytes.NewReader(pdf))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello PDF") {
		t.Errorf("text = %q, want to contain 'Hello PDF'", text)
	}
}

// Test 22: pdf.Extract against corrupt data returns error.
func TestPDFExtractor_Corrupt(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not available")
	}

	e := &PDFExtractor{}
	_, err := e.Extract(strings.NewReader("not a pdf"))
	if err == nil {
		t.Fatal("expected error for corrupt PDF")
	}
}

// Test 23: docx.Extract returns expected text.
func TestDocxExtractor(t *testing.T) {
	docx := createTestDocx(t, "Hello Docx World")
	e := &DocxExtractor{}
	text, err := e.Extract(bytes.NewReader(docx))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello Docx World") {
		t.Errorf("text = %q, want to contain 'Hello Docx World'", text)
	}
}

func TestDocxExtractor_InvalidZip(t *testing.T) {
	e := &DocxExtractor{}
	_, err := e.Extract(strings.NewReader("not a zip"))
	if err == nil {
		t.Fatal("expected error for invalid zip")
	}
}

func TestDocxExtractor_MissingDocumentXML(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	_ = w.Close()

	e := &DocxExtractor{}
	_, err := e.Extract(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("expected error for missing document.xml")
	}
}

// Test 24: Registry returns correct extractor or noop.
func TestRegistry(t *testing.T) {
	reg := NewRegistry()

	if _, ok := reg.For("text/plain").(*TextExtractor); !ok {
		t.Error("expected TextExtractor for text/plain")
	}
	if _, ok := reg.For("text/html").(*HTMLExtractor); !ok {
		t.Error("expected HTMLExtractor for text/html")
	}
	if _, ok := reg.For("application/pdf").(*PDFExtractor); !ok {
		t.Error("expected PDFExtractor for application/pdf")
	}

	// Unknown type returns noop.
	noop := reg.For("application/octet-stream")
	text, err := noop.Extract(strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Errorf("noop extractor returned %q, want empty", text)
	}
}

func createTestDocx(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>` + text + `</w:t></w:r></w:p>
  </w:body>
</w:document>`

	f, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(docXML)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func minimalPDF(text string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 100 700 Td (%s) Tj ET", text)
	pdf := fmt.Sprintf(`%%PDF-1.0
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj

2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj

3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]
   /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>
endobj

4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj

5 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj

xref
0 6
trailer
<< /Size 6 /Root 1 0 R >>
startxref
0
%%%%EOF`, len(content)+1, content)
	return []byte(pdf)
}
