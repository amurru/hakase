# Execution Plan: Telegram voice (#19) — DRAFT, post-discussion

Strategy: build the transport-neutral `internal/speech` pipeline first with
fake-binary test seams, then wire the Telegram inbound branch (mirror of the
photo path), then the optional outbound `/voice` mode. Nothing lands until
[D1]–[D5] are settled (see spec end).

## Phases

### Phase 1 — internal/speech (STT core)

1. Package skeleton: `Transcriber`, `Availability`, config struct mirroring;
   whisper pipeline (temp dir, ffmpeg→WAV 16k mono, whisper-cli -otxt,
   cleanup) with injectable binary paths + PATH-stub fake binaries in
   tests. Spec: TV-001.
2. Serialized queue (1 worker, depth 3, `ErrBusy`) + timeout. Spec: TV-001.
3. Model auto-download to `~/.hakase/models/whisper/` (atomic, size check,
   injectable URL base, httptest coverage). Spec: TV-001.

### Phase 2 — Telegram inbound

4. Config sub-blocks + validation + env (STT/TTS enabled). Spec: TV-002.
5. `handleMessage` voice branch: duration guard, download (photos.go
   mirror), echo `🎙 Heard: …`, `startRun` with transcript; degraded
   hints; `ErrBusy` message. Spec: TV-003.
6. `api` interface + fakeAPI gain `SendVoice`/`GetFile` support; run_test-
   style coverage for the full inbound path. Spec: TV-003/005.

### Phase 3 — Outbound TTS (stretch, per [D1])

7. `Synthesizer` (piper → WAV → ffmpeg → OGG) with the same fake-binary
   seams. Spec: TV-001/004.
8. `state.Chat.VoiceMode` + `/voice off|auto|on` command + help; wire mode
   into the run finalizer (markdown strip, cap, synthesize, SendVoice,
   text fallback on failure). Spec: TV-004.

### Phase 4 — Docs + landing

9. README channels section + DEVELOPMENT.md + CHANGELOG; tasks.md ticked
   in the landing PR; issue #19 acceptance walkthrough (needs a real
   Telegram round-trip by the owner).

## Critical path

1 → 2 → 5 → 6 (STT alone = shippable); 3 is independent inside Phase 1;
Phase 3 only after [D1] resolves to "wire TTS now".

## Verification baseline

- `go test ./internal/speech/ ./internal/channel/telegram/` with fake
  binaries — no real whisper/piper needed in CI.
- Live acceptance (owner): voice note → echo → answer; `/stop` mis-fire
  path; missing-binary hint; voice reply with `/voice auto`.

## Risk register

- Piper CLI flags drift between piper-tts versions — verify against the
  installed version at implementation time; config exposes the binary path
  so users can wrap version differences.
- whisper-cli output flags (`-otxt -of`) — verify against the built
  binary; stdout parsing is the fallback (no-prints mode).
- Transcription latency on CPU (base model ≈ real-time-ish for short
  notes) — serialization + echo-keepalive manages expectations; model
  size is a config lever.
- Telegram getFile 20 MB ceiling — duration guard fires first for sane
  max_seconds values.
