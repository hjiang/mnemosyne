// Package search implements Gmail-style query parsing and SQL execution.
package search

import (
	"fmt"
	"strings"
	"time"
)

// Query is the parsed representation of a search query.
type Query struct {
	Text          []string   // free-text terms (ANDed for FTS5 MATCH)
	From          string     // from: operator
	To            string     // to: operator
	Cc            string     // cc: operator
	Subject       string     // subject: operator
	HasAttachment bool       // has:attachment
	Before        *time.Time // before:YYYY-MM-DD
	After         *time.Time // after:YYYY-MM-DD
	Filename      string     // filename: operator
}

// IsEmpty returns true if no search criteria were specified.
func (q *Query) IsEmpty() bool {
	return len(q.Text) == 0 &&
		q.From == "" && q.To == "" && q.Cc == "" && q.Subject == "" &&
		!q.HasAttachment && q.Before == nil && q.After == nil && q.Filename == ""
}

// ParseError describes a syntax error in the query string.
type ParseError struct {
	Msg      string
	Pos      int
	Operator string
}

func (e *ParseError) Error() string {
	if e.Operator != "" {
		return fmt.Sprintf("at position %d: unknown operator %q", e.Pos, e.Operator)
	}
	return fmt.Sprintf("at position %d: %s", e.Pos, e.Msg)
}

// knownOps is the set of recognized operator names (lowercase).
var knownOps = map[string]bool{
	"from": true, "to": true, "cc": true, "subject": true,
	"has": true, "before": true, "after": true, "filename": true,
}

// Parse parses a Gmail-style query string into a Query.
func Parse(input string) (*Query, error) {
	q := &Query{}
	p := &parser{input: input}

	for {
		p.skipSpaces()
		if p.pos >= len(p.input) {
			break
		}

		// Check for operator:value pattern.
		if op, val, consumed, err := p.tryOperator(); consumed {
			if err != nil {
				return nil, err
			}
			if err := q.applyOperator(op, val, p.pos-len(val)-len(op)-1); err != nil {
				return nil, err
			}
			continue
		}

		// Free text (possibly quoted).
		val, err := p.readValue()
		if err != nil {
			return nil, err
		}
		if val != "" {
			q.Text = append(q.Text, val)
		}
	}

	return q, nil
}

func (q *Query) applyOperator(op, val string, pos int) error {
	switch op {
	case "from":
		q.From = val
	case "to":
		q.To = val
	case "cc":
		q.Cc = val
	case "subject":
		q.Subject = val
	case "has":
		if strings.EqualFold(val, "attachment") {
			q.HasAttachment = true
		}
	case "before":
		t, err := time.Parse("2006-01-02", val)
		if err != nil {
			return &ParseError{Msg: fmt.Sprintf("invalid date %q (expected YYYY-MM-DD)", val), Pos: pos}
		}
		q.Before = &t
	case "after":
		t, err := time.Parse("2006-01-02", val)
		if err != nil {
			return &ParseError{Msg: fmt.Sprintf("invalid date %q (expected YYYY-MM-DD)", val), Pos: pos}
		}
		q.After = &t
	case "filename":
		q.Filename = val
	default:
		return &ParseError{Operator: op, Pos: pos}
	}
	return nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) skipSpaces() {
	for p.pos < len(p.input) && p.input[p.pos] == ' ' {
		p.pos++
	}
}

// tryOperator attempts to read an "operator:value" token.
// Returns the operator (lowercase), value, whether anything was consumed, and any error.
func (p *parser) tryOperator() (string, string, bool, error) {
	// Look ahead for a colon that's part of operator:value.
	start := p.pos
	i := p.pos
	for i < len(p.input) && p.input[i] != ' ' && p.input[i] != ':' && p.input[i] != '"' {
		i++
	}
	if i >= len(p.input) || p.input[i] != ':' || i == start {
		return "", "", false, nil
	}

	op := strings.ToLower(p.input[start:i])
	if !knownOps[op] {
		// Unknown operator — report error.
		return "", "", true, &ParseError{Operator: op, Pos: start}
	}

	p.pos = i + 1 // skip the colon
	val, err := p.readValue()
	if err != nil {
		return "", "", true, err
	}
	return op, val, true, nil
}

// readValue reads the next token: either a quoted string or an unquoted word.
func (p *parser) readValue() (string, error) {
	if p.pos >= len(p.input) {
		return "", nil
	}

	if p.input[p.pos] == '"' {
		return p.readQuoted()
	}
	return p.readWord(), nil
}

func (p *parser) readQuoted() (string, error) {
	start := p.pos
	p.pos++ // skip opening quote
	var b strings.Builder
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == '"' {
			p.pos++ // skip closing quote
			return b.String(), nil
		}
		b.WriteByte(ch)
		p.pos++
	}
	return "", &ParseError{Msg: "unclosed quote", Pos: start}
}

func (p *parser) readWord() string {
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != ' ' {
		p.pos++
	}
	return p.input[start:p.pos]
}
