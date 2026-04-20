# Deferred cleanup tasks

Items identified during a refactor review but deferred as lower priority
or higher risk.

## Structured DB error types
Replace `strings.Contains(err.Error(), "FOREIGN KEY")` in
`internal/messages/repo.go:86` and similar string-based checks in
`internal/users/repo.go` with typed errors. The current string matching
is brittle against driver version bumps.

## Orchestrator `syncFolder` breakdown
`internal/backup/orchestrator.go` `syncFolder` is ~200 lines with
intertwined state. Split into smaller functions to improve testability.
Higher-risk change requiring careful test coverage.

## Envelope merge duplication in orchestrator
Two near-identical dedup/merge patterns in the orchestrator. Extract a
shared helper once `syncFolder` is broken up.

## Scan consolidation in repos
`messages/repo.go` and `jobs/queue.go` each repeat similar Scan calls
4 times. Could extract scan helpers, but the coupling between SELECT
column order and Scan order introduces a runtime footgun without
compile-time safety. Marginal net benefit.

## context.Context propagation
No repo method currently takes `context.Context`. Adding it enables
query cancellation and timeouts but is a cross-cutting change touching
every handler.
