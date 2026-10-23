---
name: go-test-engineering
description: Design, write, and review Go tests in this repository. Use for deciding unit versus WebSocket integration coverage, testing async game state, race-sensitive managers, protocol framing, provider fakes, or regressions in the turtle soup server.
metadata:
  short-description: Go unit, WebSocket integration, race, and eval testing
---

# Go test engineering

## Choose the smallest test layer

- Pure verdict parsing, codec framing, config validation, state transitions, and timer calculations: table-driven unit tests with no network.
- WebSocket hello/listen/tts contracts, room admission, disconnect behavior, and staged game flow: one `httptest.Server` integration test with real Gorilla WebSocket.
- Real ASR/LLM/TTS quality, latency, or provider credentials: `cmd/eval`, never normal unit tests.

Do not duplicate one behavior across unit, integration, and E2E tests. Extend an existing staged scenario before adding another near-identical case.

## Required invariants

- Unit tests use only in-process fakes or `httptest.Server`; no real provider calls.
- Async assertions use channels, eventually conditions with a bounded timeout, or observable protocol events. Never use `time.Sleep` as proof.
- Assert the shared state or emitted event, not only that a goroutine started or a function returned.
- Concurrent state must be protected or atomic; run `go test -race`.
- WebSocket tests must read all protocol stages and assert their order, not just the final verdict.
- Failing provider credentials must fail the explicit eval run, not silently skip it.
- Avoid mocks of internal game logic. Fake only ASR, TTS, judge, codec, or external HTTP boundaries.

## Commands

```bash
cd server
gofmt -l .
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go run ./cmd/eval -config config.local.json
go run ./cmd/eval -config config.local.json -e2e
```

Use a focused command while iterating:

```bash
go test ./internal/game -run TestName -count=1
go test ./internal/api -run TestName -count=1 -v
```

For `staticcheck`, keep caches outside the repo:

```bash
GOBIN=/tmp/codex-go-bin GOPROXY=https://proxy.golang.org,direct \
  go install honnef.co/go/tools/cmd/staticcheck@latest
GOCACHE=/tmp/codex-staticcheck-cache GOMODCACHE=/tmp/codex-staticcheck-mod \
  /tmp/codex-go-bin/staticcheck ./...
```

## Reporting

State which layer ran and what it proves. Separate deterministic unit/integration results from live provider eval results. Do not present a mocked TTS/ASR pass as a complete voice chain.
