package extract

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

// HTMLExtractor extracts visible text from HTML documents.
type HTMLExtractor struct{}

// Extract parses HTML and returns visible text, excluding script/style content.
func (e *HTMLExtractor) Extract(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	extractText(doc, &b, false)
	return strings.TrimSpace(b.String()), nil
}

func extractText(n *html.Node, b *strings.Builder, skip bool) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style":
			skip = true
		}
	}

	if n.Type == html.TextNode && !skip {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(text)
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractText(c, b, skip)
	}
}
