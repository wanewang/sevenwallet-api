# Auto-regenerate OpenAPI spec on commit (lefthook) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A lefthook `pre-commit` hook that runs `make docs` and stages the regenerated OpenAPI spec whenever a commit touches a spec-affecting file, so the committed spec is always current.

**Architecture:** lefthook is pinned as a Go `tool` dependency (like swag). A checked-in `lefthook.yml` defines a glob-filtered `pre-commit` command that runs `make docs` and `git add`s the three generated spec files. A `make hooks` target installs the git hooks. The existing CI `make docs-check` stays as the enforcement backstop.

**Tech Stack:** Go 1.25 (`tool` directive), lefthook, the existing `make docs` target (swaggo).

## Global Constraints

- Go version floor: `go 1.25.0` (from `go.mod`).
- Public repo: commit messages may keep `Co-Authored-By:` and the `🤖 Generated with Claude Code` line, but MUST NOT include any `Claude-Session:` / `https://claude.ai/code/session_...` URL.
- Branch base: this work branches off `feature/api-docs` (PR #4) because `make docs` does not exist on `main` yet.
- lefthook is pinned via the Go 1.24+ `tool` directive (`go get -tool`); no global install. Require lefthook ≥ 1.7 (its generated hooks include a `go tool lefthook` runner fallback).
- The hook auto-stages the generated spec via an explicit `git add` (NOT lefthook `stage_fixed`, which targets the command's input files, not the separately-generated spec files).
- Do not modify the `docs` / `docs-check` targets or any application code; CI `docs-check` remains the backstop.

## File Structure

**New:**
- `lefthook.yml` (repo root) — the `pre-commit` hook definition.

**Modified:**
- `go.mod` / `go.sum` — `tool` directive pinning lefthook.
- `Makefile` — add a `hooks` target; update `.PHONY`.
- `README.md` — note to run `make hooks` once.

---

## Task 1: lefthook pre-commit auto-regeneration

**Files:**
- Modify: `go.mod`, `go.sum` (via `go get -tool`)
- Create: `lefthook.yml`
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Consumes: the existing `make docs` target (regenerates `internal/apidocs/swagger.json`, `internal/apidocs/swagger.yaml`, `docs/api/openapi.json`).
- Produces: a `make hooks` target (`go tool lefthook install`) and a `lefthook.yml` `pre-commit` config.

- [ ] **Step 1: Add lefthook as a Go tool dependency**

Run:
```bash
go get -tool github.com/evilmartians/lefthook@latest
```
Expected: `go.mod` gains a `tool github.com/evilmartians/lefthook` directive and a matching `require` (a v1.7+ version); `go.sum` updates.

Verify the tool runs:
```bash
go tool lefthook version
```
Expected: prints a version `1.7.x` or newer.

- [ ] **Step 2: Create lefthook.yml**

Create `lefthook.yml` at the repo root:

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

(`glob` restricts the command to commits touching spec-affecting files; `run` regenerates the spec and explicitly stages the three generated files into the commit.)

- [ ] **Step 3: Add the `hooks` make target**

In `Makefile`, change the first line:
```makefile
.PHONY: docs docs-check
```
to:
```makefile
.PHONY: docs docs-check hooks
```

And append at the end of the file:
```makefile

# Install git hooks (lefthook) — run once per clone.
hooks:
	go tool lefthook install
```

- [ ] **Step 4: Install and verify the hook wiring**

Run:
```bash
make hooks
```
Expected: lefthook reports it synced/installed hooks; `.git/hooks/pre-commit` now exists.

Verify the installed hook can locate lefthook via the Go tool fallback:
```bash
test -f .git/hooks/pre-commit && echo "hook installed"
grep -q 'go tool lefthook' .git/hooks/pre-commit && echo "go-tool fallback present"
```
Expected: both lines print. (If `go tool lefthook` is NOT present in the generated hook, the installed lefthook version is too old — re-run Step 1 with a newer version, e.g. `@v1.11.0`, and re-run `make hooks`.)

- [ ] **Step 5: Verify the hook runs cleanly on a clean tree**

Run:
```bash
go tool lefthook run pre-commit
```
Expected: exits 0. On a clean working tree nothing spec-affecting is staged, so the `openapi-docs` command is skipped by its glob (or runs `make docs` to a no-op) — either way, exit 0 with no staged changes.

Confirm the application is unaffected:
```bash
go build ./... && go vet ./... && go test ./... && make docs-check
```
Expected: build/vet clean; tests PASS (store/rediscache skip without env vars); `docs-check` exits 0.

- [ ] **Step 6: Update the README**

In `README.md`, under the `## API documentation` section, after the line listing the `make docs` / `make docs-check` commands, add a short note (a normal paragraph, not inside the code block):

> After cloning, run `make hooks` once to install a pre-commit hook that regenerates and stages the spec automatically when you change a handler annotation or a `wallet` type.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum lefthook.yml Makefile README.md
git commit -m "build: add lefthook pre-commit to auto-regenerate the OpenAPI spec

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

(Note: this commit touches `cmd/server/main.go`? No — it does not. The glob will not trigger on this commit, so committing does not itself regenerate the spec. That is correct.)

---

## Final verification

These are behavioral smoke checks (git hooks are not unit-testable). Each one
restores the working tree to its starting state — run `git status --porcelain`
before starting and confirm it is empty.

- [ ] **Auto-regen + stage works (reversible smoke).**

  ```bash
  git status --porcelain            # must be empty before starting
  # Temporarily change the API description so the generated spec actually changes:
  #   edit cmd/server/main.go and change the `// @description ...` text.
  git add cmd/server/main.go
  go tool lefthook run pre-commit   # runs make docs && git add (glob matches)
  git diff --cached --name-only     # EXPECT: includes internal/apidocs/swagger.json + docs/api/openapi.json
  # Clean up — restore everything to the committed state:
  git restore --staged cmd/server/main.go internal/apidocs/ docs/api/openapi.json
  git checkout -- cmd/server/main.go internal/apidocs/ docs/api/openapi.json
  git status --porcelain            # must be empty again
  ```
  Expected: the regenerated `swagger.json` and `openapi.json` appear in
  `git diff --cached --name-only`, proving the spec was auto-staged; the tree is
  clean again after cleanup.

- [ ] **Glob skip works (unrelated file does not run swag).**

  ```bash
  git status --porcelain            # must be empty
  printf '\n' >> README.md
  git add README.md
  go tool lefthook run pre-commit   # openapi-docs command is skipped (glob no-match)
  git diff --cached --name-only     # EXPECT: only README.md (no swagger.json)
  git restore --staged README.md && git checkout -- README.md
  git status --porcelain            # must be empty again
  ```
  Expected: only `README.md` is staged; the spec files are not touched, confirming
  the glob filter skips non-spec commits.

- [ ] **Full suite green:** `go build ./... && go vet ./... && go test ./... && make docs-check` — all PASS/exit 0.
