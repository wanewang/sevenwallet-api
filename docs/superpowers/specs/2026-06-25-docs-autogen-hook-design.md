# Auto-regenerate OpenAPI spec on commit (lefthook) — Design

**Date:** 2026-06-25
**Status:** Approved (design); implementation plan available

## Summary

Add a [lefthook](https://github.com/evilmartians/lefthook) `pre-commit` hook that
automatically runs `make docs` and stages the regenerated OpenAPI spec whenever a
commit touches a spec-affecting file. The committed spec is then always current
without anyone having to remember `make docs`. The existing CI `make docs-check`
remains the hard enforcement backstop.

## Goals

- Local commits that change handler annotations (or the Go types the spec is
  generated from) automatically include an up-to-date spec.
- Zero-friction: contributors run a single install step once; nothing to remember
  per commit.
- Cheap: the hook only runs swag when relevant files changed.
- No new global/external install — lefthook is pinned like the existing `swag`
  tool dependency.

## Non-goals

- Not removing or weakening the CI `docs-check` guard (the hook is local and can
  be bypassed with `--no-verify` or simply not installed; CI is the guarantee).
- No CI auto-commit bot.
- No change to `make docs` / `make docs-check` themselves.

## Background

`make docs` regenerates
`internal/apidocs/swagger.{json,yaml}` and copies the JSON to
`docs/api/openapi.json`. `make docs-check` regenerates and `git diff --exit-code`s
to fail on drift; it runs in CI. The OpenAPI spec is generated from swaggo
annotations on `cmd/server/main.go` (general info) and
`internal/api/handlers.go` (per-endpoint), with response schemas derived from the
`internal/wallet` types. swag is pinned via the Go 1.24+ `tool` directive.

**Base branch:** main.

## Components

### 1. lefthook as a Go tool dependency

Add lefthook via the same `tool`-directive pattern as swag:

```
go get -tool github.com/evilmartians/lefthook
```

This adds a `tool github.com/evilmartians/lefthook` directive to `go.mod` (and the
require). lefthook is invoked as `go tool lefthook`; the git hooks lefthook
installs include a `go tool lefthook` fallback, so no global install is required.

### 2. `lefthook.yml` (repo root)

```yaml
pre-commit:
  commands:
    openapi-docs:
      glob:
        - "cmd/server/main.go"
        - "internal/api/handlers.go"
        - "internal/wallet/*.go"
      run: make docs && git add internal/apidocs/swagger.json internal/apidocs/swagger.yaml docs/api/openapi.json
```

- `glob` limits the command to commits that touch spec-affecting files, so most
  commits skip swag entirely (no ~1–2s cost).
- `run` regenerates the spec via the existing target, then explicitly
  `git add`s the three generated files so they are staged into the in-progress
  commit.
- **Why explicit `git add` and not lefthook's `stage_fixed: true`:**
  `stage_fixed` re-stages the command's *input* files (the formatter pattern —
  files matching `glob` that the command modified in place, e.g. gofmt rewriting
  the staged `.go` file). Here `make docs` modifies *different* files (the
  generated spec), which are **not** in the `glob` set, so `stage_fixed` would
  not stage them. The explicit `git add` of the generated paths is the reliable
  way to get them into the commit.

### 3. `make hooks` target

Add to the `Makefile`:

```makefile
hooks:
	go tool lefthook install
```

Run once per clone to wire `.git/hooks`. (`.PHONY` updated to include `hooks`.)

### 4. README

Add one line under the existing API documentation section: run `make hooks` once
to enable automatic spec regeneration on commit.

## Data flow

```
git commit
   │  (lefthook pre-commit)
   ▼
touches cmd/server/main.go | internal/api/handlers.go | internal/wallet/*.go ?
   │ yes                                        │ no
   ▼                                            ▼
make docs -> regenerate spec                 skip (no swag run)
   │  stage_fixed: true
   ▼
regenerated swagger.json/yaml + openapi.json staged into the commit
```

## Error handling

- If `make docs` fails (e.g. swag error from a malformed annotation), the hook
  exits non-zero and the commit is aborted — the developer sees the swag error
  and fixes the annotation. This is desirable.
- If `make hooks` was never run, no hook exists; commits proceed normally and CI
  `docs-check` still catches any drift.
- `--no-verify` bypasses the hook intentionally; CI remains the backstop.

## Testing

Git hooks are not unit-testable; verification is a scripted behavioral check on a
throwaway commit (cleaned up afterward), confirming:

1. After `make hooks`, editing a handler annotation and committing causes the
   regenerated spec to be staged into that commit (the commit includes the spec
   changes).
2. A commit touching only an unrelated file (e.g. `README.md`) does **not** run
   swag (the `glob` filter skips it).
3. `go build ./...`, `go vet ./...`, `go test ./...`, and `make docs-check` still
   pass (the hook setup changes no application code).

The verification script must restore the working tree/branch state it started
from (no leftover test commits).

## Decisions

- **Auto-stage, not fail-and-tell:** the hook regenerates and `git add`s the
  spec so the commit always carries a current spec, rather than aborting and
  asking the developer to re-stage. (Fail-and-tell is the rejected alternative —
  safer but adds a manual step, defeating the "never think about it" goal; CI
  `docs-check` already provides the fail-loudly behavior.)
- **lefthook over a hand-written `.git/hooks` script:** lefthook is checked in
  (shareable across clones), handles glob-filtering and re-staging declaratively,
  and pins cleanly via the `tool` directive. A raw hook script would re-implement
  glob + staging by hand and isn't shared automatically.

## Future options (out of scope)

- A `pre-push` hook running `make docs-check` for an extra local guard.
- Additional lefthook commands (e.g. `gofmt`, `go vet`) — separate decision.
