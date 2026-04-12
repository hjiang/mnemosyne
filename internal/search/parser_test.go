package search

import (
	"testing"
	"time"
)

func TestParse_EmptyQuery(t *testing.T) {
	q, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Text) != 0 || q.From != "" || q.To != "" || q.Subject != "" {
		t.Errorf("expected empty query, got %+v", q)
	}
}

func TestParse_FreeText(t *testing.T) {
	q, err := Parse("budget")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Text) != 1 || q.Text[0] != "budget" {
		t.Errorf("Text = %v, want [budget]", q.Text)
	}
}

func TestParse_FromOperator(t *testing.T) {
	q, err := Parse("from:alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if q.From != "alice@example.com" {
		t.Errorf("From = %q, want alice@example.com", q.From)
	}
}

func TestParse_Combined(t *testing.T) {
	q, err := Parse(`from:alice subject:"quarterly report" budget`)
	if err != nil {
		t.Fatal(err)
	}
	if q.From != "alice" {
		t.Errorf("From = %q, want alice", q.From)
	}
	if q.Subject != "quarterly report" {
		t.Errorf("Subject = %q, want 'quarterly report'", q.Subject)
	}
	if len(q.Text) != 1 || q.Text[0] != "budget" {
		t.Errorf("Text = %v, want [budget]", q.Text)
	}
}

func TestParse_HasAttachment(t *testing.T) {
	q, err := Parse("has:attachment")
	if err != nil {
		t.Fatal(err)
	}
	if !q.HasAttachment {
		t.Error("expected HasAttachment = true")
	}
}

func TestParse_BeforeDate(t *testing.T) {
	q, err := Parse("before:2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if q.Before == nil || !q.Before.Equal(want) {
		t.Errorf("Before = %v, want %v", q.Before, want)
	}
}

func TestParse_BeforeDate_Invalid(t *testing.T) {
	_, err := Parse("before:not-a-date")
	if err == nil {
		t.Fatal("expected error for invalid date")
	}
	if pe, ok := err.(*ParseError); ok {
		if pe.Pos < 0 {
			t.Error("expected positive position in error")
		}
	} else {
		t.Errorf("expected *ParseError, got %T", err)
	}
}

func TestParse_Filename(t *testing.T) {
	q, err := Parse("filename:*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filename != "*.pdf" {
		t.Errorf("Filename = %q, want *.pdf", q.Filename)
	}
}

func TestParse_UnknownOperator(t *testing.T) {
	_, err := Parse("foo:bar")
	if err == nil {
		t.Fatal("expected error for unknown operator")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if pe.Operator != "foo" {
		t.Errorf("error operator = %q, want foo", pe.Operator)
	}
}

func TestParse_UnclosedQuote(t *testing.T) {
	_, err := Parse(`subject:"unclosed`)
	if err == nil {
		t.Fatal("expected error for unclosed quote")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if pe.Pos < 0 {
		t.Error("expected positive position in error")
	}
}

func TestParse_WhitespaceHandling(t *testing.T) {
	q, err := Parse("   budget   report   ")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Text) != 2 || q.Text[0] != "budget" || q.Text[1] != "report" {
		t.Errorf("Text = %v, want [budget report]", q.Text)
	}
}

func TestParse_CaseInsensitiveOperators(t *testing.T) {
	q, err := Parse("FROM:Alice")
	if err != nil {
		t.Fatal(err)
	}
	if q.From != "Alice" {
		t.Errorf("From = %q, want Alice (operator case-insensitive, value preserved)", q.From)
	}
}

func TestParse_ToAndCc(t *testing.T) {
	q, err := Parse("to:bob@test.com cc:carol@test.com")
	if err != nil {
		t.Fatal(err)
	}
	if q.To != "bob@test.com" {
		t.Errorf("To = %q, want bob@test.com", q.To)
	}
	if q.Cc != "carol@test.com" {
		t.Errorf("Cc = %q, want carol@test.com", q.Cc)
	}
}

func TestParse_AfterDate(t *testing.T) {
	q, err := Parse("after:2025-06-15")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	if q.After == nil || !q.After.Equal(want) {
		t.Errorf("After = %v, want %v", q.After, want)
	}
}

func TestParse_MultipleFreeText(t *testing.T) {
	q, err := Parse("hello world from:alice goodbye")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Text) != 3 {
		t.Fatalf("Text = %v, want 3 terms", q.Text)
	}
	if q.Text[0] != "hello" || q.Text[1] != "world" || q.Text[2] != "goodbye" {
		t.Errorf("Text = %v, want [hello world goodbye]", q.Text)
	}
	if q.From != "alice" {
		t.Errorf("From = %q, want alice", q.From)
	}
}

func TestParse_QuotedFreeText(t *testing.T) {
	q, err := Parse(`"exact phrase"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Text) != 1 || q.Text[0] != "exact phrase" {
		t.Errorf("Text = %v, want ['exact phrase']", q.Text)
	}
}
