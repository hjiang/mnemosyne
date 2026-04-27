# Deferred cleanup tasks

Items identified during a refactor review but deferred as lower priority
or higher risk.

## Orchestrator `syncFolder` breakdown
`internal/backup/orchestrator.go` `syncFolder` is ~200 lines with
intertwined state. Split into smaller functions to improve testability.
Higher-risk change requiring careful test coverage.

## Envelope merge duplication in orchestrator
Two near-identical dedup/merge patterns in the orchestrator. Extract a
shared helper once `syncFolder` is broken up.

## context.Context propagation
No repo method currently takes `context.Context`. Adding it enables
query cancellation and timeouts but is a cross-cutting change touching
every handler.

## Browse HTMX partial renders empty
`internal/httpserver/browse.go` (`browseHandler`) handles
`HX-Request: true` by executing `s.templates["browse_messages.html"]`,
but `internal/httpserver/templates/browse_messages.html` wraps its
entire body in `{{define "browse_messages_content"}}...{{end}}`. As a
result, the file-named outer template is empty and `tmpl.Execute(...)`
returns no body — HTMX folder navigation likely shows blank content.

Two plausible fixes:
1. Drop the `{{define}}` wrapper in `browse_messages.html` so the file
   body is the template body, and update the parent `browse.html`
   include accordingly (it currently uses `{{template "browse_messages_content" .}}`).
2. Change the handler to call `tmpl.ExecuteTemplate(w, "browse_messages_content", data)`
   instead of `tmpl.Execute(w, data)`.

Write a regression test that asserts the partial response contains a
seeded message subject before fixing — the gap was found while adding
tests in `browse_test.go` and the partial test was dropped rather than
codify the broken behavior.
