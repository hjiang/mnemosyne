package search

import (
	"database/sql"
	"fmt"
	"strings"
)

// MessageResult is a single search result row.
type MessageResult struct {
	Hash           []byte
	MessageID      string
	FromAddr       string
	ToAddrs        string
	CcAddrs        string
	Subject        string
	Date           *int64
	Size           int64
	HasAttachments bool
	BodyText       string
}

// Executor converts parsed queries to SQL and runs them.
type Executor struct {
	db *sql.DB
}

// NewExecutor creates a search executor.
func NewExecutor(db *sql.DB) *Executor {
	return &Executor{db: db}
}

// Search runs a parsed query scoped to the given user.
// enforces user isolation
func (e *Executor) Search(q *Query, userID int64) ([]MessageResult, error) {
	where := []string{"m.user_id = ?"}
	args := []any{userID}

	if len(q.Text) > 0 {
		match := buildFTSMatch(q.Text)
		where = append(where, "m.rowid IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)")
		args = append(args, match)
	}

	if q.From != "" {
		where = append(where, "m.from_addr LIKE ?")
		args = append(args, "%"+escapeLike(q.From)+"%")
	}

	if q.To != "" {
		where = append(where, "m.to_addrs LIKE ?")
		args = append(args, "%"+escapeLike(q.To)+"%")
	}

	if q.Cc != "" {
		where = append(where, "m.cc_addrs LIKE ?")
		args = append(args, "%"+escapeLike(q.Cc)+"%")
	}

	if q.Subject != "" {
		where = append(where, "m.subject LIKE ?")
		args = append(args, "%"+escapeLike(q.Subject)+"%")
	}

	if q.HasAttachment {
		where = append(where, "m.has_attachments = 1")
	}

	if q.Before != nil {
		where = append(where, "m.date < ?")
		args = append(args, q.Before.Unix())
	}

	if q.After != nil {
		where = append(where, "m.date > ?")
		args = append(args, q.After.Unix())
	}

	if q.Filename != "" {
		where = append(where, "m.rowid IN (SELECT m2.rowid FROM messages m2 JOIN attachments a ON a.message_hash = m2.hash WHERE a.filename LIKE ?)")
		args = append(args, "%"+escapeLike(q.Filename)+"%")
	}

	query := fmt.Sprintf( //nolint:gosec // WHERE clause is built from parameterized predicates, not user input
		`SELECT m.hash, m.message_id, m.from_addr, m.to_addrs, m.cc_addrs, m.subject, m.date, m.size, m.has_attachments, m.body_text
		 FROM messages m
		 WHERE %s
		 ORDER BY m.date DESC
		 LIMIT 200`,
		strings.Join(where, " AND "))

	rows, err := e.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("executing search: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var results []MessageResult
	for rows.Next() {
		var r MessageResult
		var hasAtt int
		var messageID, fromAddr, toAddrs, ccAddrs, subject, bodyText sql.NullString
		if err := rows.Scan(&r.Hash, &messageID, &fromAddr, &toAddrs, &ccAddrs,
			&subject, &r.Date, &r.Size, &hasAtt, &bodyText); err != nil {
			return nil, fmt.Errorf("scanning result: %w", err)
		}
		r.MessageID = messageID.String
		r.FromAddr = fromAddr.String
		r.ToAddrs = toAddrs.String
		r.CcAddrs = ccAddrs.String
		r.Subject = subject.String
		r.HasAttachments = hasAtt == 1
		r.BodyText = bodyText.String
		results = append(results, r)
	}
	return results, rows.Err()
}

// buildFTSMatch constructs an FTS5 MATCH expression from free-text terms.
// Each term is quoted to prevent FTS5 query syntax injection.
func buildFTSMatch(terms []string) string {
	escaped := make([]string, len(terms))
	for i, term := range terms {
		escaped[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(escaped, " ")
}

// escapeLike escapes SQL LIKE wildcards in user input.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
