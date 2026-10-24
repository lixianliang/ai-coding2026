---
name: quick-pr
description: Verify and submit changes in this ai-coding2026 repository through a GitHub pull request. Use when the user asks to commit, push, or open a PR for work in this repo; do not use for generic Git questions or unrelated repositories.
---

# Quick PR

Ship only the changes the current request produced. Do not "clean up" or include unrelated working-tree changes.

Every change to this repository must land through a `codex/*` branch and a pull request. Never commit or push directly to `main`, including docs and skill-only changes.

## Workflow

1. Read the root and nearest `AGENTS.md`. Inspect `git status --short`, the current branch, and `git remote -v`.
2. Identify the exact paths belonging to the task. Never use `git add -A`, `git add .`, or `git commit -a`.
3. Create the next newest `codex/<short-topic>` branch from `HEAD` when the checkout is on `main`:
   ```bash
   git switch -c codex/<short-topic>
   ```
   Keep existing uncommitted work intact.
4. Run the checks that apply to the changed area. For Go changes under `server/`:
   ```bash
   cd server
   gofmt -w .
   go vet ./...
   go test ./...
   go test -race ./...
   git diff --check
   ```
   Root policy requires `staticcheck`. If it is not installed, install it to `/tmp` and run it with temporary caches:
   ```bash
   GOBIN=/tmp/codex-go-bin GOPROXY=https://proxy.golang.org,direct \
     go install honnef.co/go/tools/cmd/staticcheck@latest
   GOCACHE=/tmp/codex-staticcheck-cache GOMODCACHE=/tmp/codex-staticcheck-mod \
     /tmp/codex-go-bin/staticcheck ./...
   ```
   If any required check fails, fix it or stop. Do not submit a knowingly failing change.
5. Stage only the task paths, then review what will be committed:
   ```bash
   git add -- <task-paths>
   git diff --cached --check
   git diff --cached --stat
   git diff --cached
   ```
6. Commit with a conventional message that describes the delivered change. Do not amend or force-push unless the user explicitly asks.
7. Push the `codex/` branch to `origin`, then open a PR against `main`:
   ```bash
   git push -u origin "$(git branch --show-current)"
   repo=$(gh repo view --json nameWithOwner -q .nameWithOwner)
   gh pr create --repo "$repo" --base main --title "<title>" --body "<summary, checks, limits>"
   ```
   If an open PR already exists for the branch, reuse it and report its URL instead of creating another.
8. Report the PR URL, commit, files submitted, checks run, and any unverified area or known limitation.

## Guardrails

- Never commit or push directly to `main`.
- Leave unrelated modified and untracked files out of the index and commit.
- Do not commit credentials, local device configuration, databases, logs, or build artifacts.
- Do not claim hardware, cloud-provider, or end-to-end validation unless it was actually run.
- If authentication, push, or PR creation fails, stop with the exact blocker and leave the verified branch intact.
