---
name: go-provider-debugging
description: Diagnose Qwen/Doubao ASR, anonymous speaker attribution, DeepSeek judge, MiniMax TTS, and Opus/PCM voice-pipeline failures in this repository. Use for provider configuration, transcript/verdict/audio debugging, latency checks, and end-to-end speech-chain validation; not for unrelated provider integrations.
metadata:
  short-description: ASR, LLM, TTS, and voice-chain debugging
---

# Go provider debugging

## Keep secrets local

All runtime configuration belongs in `server/config.local.json`. The file is ignored and must stay mode `600`. Never print API keys, commit them, or paste device/cloud identifiers into logs or PR text.

```bash
cd server
cp config.example.json config.local.json
chmod 600 config.local.json
```

Provider boundaries in this repository:

- Qwen Realtime WebSocket: raw PCM 16 kHz mono s16le -> final transcript (no speaker identity)
- Doubao ASR 2.0 WebSocket: PCM -> transcript + session-scoped speaker ID; keep one connection through manual turns
- DeepSeek chat completion: surface + answer + transcript -> one verdict, never player identity
- MiniMax streaming TTS: speech text -> PCM fragments
- Game socket: device Opus <-> server PCM

## Locate the failing stage

Run the smallest check that isolates the boundary:

```bash
# Judge only: ASR and TTS are not called.
go run ./cmd/eval -config config.local.json

# TTS -> configured ASR -> DeepSeek -> MiniMax TTS.
go run ./cmd/eval -config config.local.json -e2e
```

For one local game turn, start the server and use the browser playground:

```bash
go run ./cmd/server
open http://127.0.0.1:8080/playground
```

Interpret failures by stage:

- ASR open/ready timeout: URL, model, key, or upstream WebSocket.
- Empty transcript: input is not 16 kHz mono s16le, too short, silent, or VAD/commit did not finalize.
- Judge parse error: provider returned non-JSON or an unknown verdict; inspect the returned content without exposing credentials.
- TTS failure: endpoint, group/voice/model, or response format mismatch.
- Device hears clicks/garbage: check Opus framing, sample rate, frame duration, and PCM byte order before changing the LLM.

## Game-chain checks

Use deterministic protocol tests for the welcome prompt, `开始游戏`, manual start/stop, audio before/after capture, duplicate room rejection, verdict speech, fixed answer, discussion state, `游戏继续`, `退出游戏`, and both timeouts. Check configured question IDs, updated surface/answer text, and removal of disabled questions when changing the question bank. Use the provider eval for real model behavior; use a physical device only for Linx MCP cutover, microphone, speaker, BOOT, and network behavior.

When reporting, include the exact command, pass count, and observed latency. Call out whether the result was mocked, provider-backed, browser-backed, or physical-device-backed.

For anonymous player scoring, run `go run ./cmd/speaker-eval -config config.local.json -pcm /tmp/a1.pcm,/tmp/b1.pcm,/tmp/a2.pcm -speakers A,B,A -pause 5s`. Check same-person stability, different-person separation, no scoring for missing/mixed speakers, and per-game reset. Distinguish synthetic voices from real human acceptance. Doubao silent keepalive preserves session clustering but adds billable ASR duration. Do not replace missing speaker IDs with LLM name extraction or per-turn numbering.

For stale spoken prompts, compare the live process start time/build with the checked-out code, then verify the actual `tts sentence_start` after restarting. For startup artifacts, distinguish PCM clipping, initial buffer underrun, and shared Opus encoder state; each host speech needs its own encoder to match the device decoder reset. Streaming callbacks include paced playback, so speech deadlines must cover the utterance duration. For Doubao manual-stop hangs, test speech followed by several seconds of silence before stop: definite results may arrive before stop and the optimized endpoint may send no further unchanged results.
