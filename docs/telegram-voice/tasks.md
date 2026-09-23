# Tasks: Telegram voice (#19)

Voice notes in (whisper.cpp, local), optional voice out (Piper, local).
Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). Decisions [D1]–[D5]
settled 2026-09-23 per the spec's recommendations: STT ships first with the
Synthesizer seam implemented but unwired; `base-q5_1` default model;
auto-download; `/voice off|auto|on` semantics reserved for the TTS phase;
audio never persisted.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — internal/speech

- [x] **T1.1 [BE]** Transcriber + Availability + whisper pipeline with
      config-resolved binaries (exec safety: LookPath + `&exec.Cmd{Path,
      Args}` struct literals, no shell, no request data in argv). Spec:
      TV-001.
- [x] **T1.2 [BE]** Serialized queue (1 worker, depth 3, ErrBusy) +
      timeout helper. Spec: TV-001.
- [x] **T1.3 [BE]** Model auto-download (atomic 0600 install, size sanity
      floor, injectable URL base). Spec: TV-001.
- [x] **T1.4 [QA]** speech tests with fake binaries + httptest download
      (happy path, missing-binary naming, duration cap, download cache +
      tiny-payload refusal, queue busy, piper seam). Spec: TV-005.

## Phase 2 — Telegram inbound

- [x] **T2.1 [BE]** Config sub-blocks (speech_to_text/text_to_speech) +
      defaults + validation + `HAKASE_TELEGRAM_{STT,TTS}_ENABLED` env.
      Spec: TV-002.
- [x] **T2.2 [BE]** Voice branch in handleMessage (duration guard,
      download via GetFile + file URL, echo `🎙 Heard:` before the run,
      startRun with transcript, degraded hints, busy message, empty-
      transcript message). Spec: TV-003.
- [x] **T2.3 [BE]** fakeAPI GetFile stub generalized for download tests
      (SendVoice arrives with the TTS phase). Spec: TV-003.
- [x] **T2.4 [QA]** inbound path tests (echo precedes run, transcript
      prompt, disabled hint, duration refuse, busy/failure/empty). Spec:
      TV-005.

## Phase 3 — Outbound TTS (stretch per [D1])

- [x] **T3.1 [BE]** Synthesizer seam (PiperTTS: piper → WAV → OGG) with
      fake-binary tests — implemented, transport wiring deferred. Spec:
      TV-001/004.
- [ ] **T3.2 [BE]** `state.Chat.VoiceMode` + `/voice off|auto|on` command +
      finalizer wiring (strip markdown, cap, synthesize, SendVoice, text
      fallback). Spec: TV-004. **Deferred — the follow-up PR.**
- [ ] **T3.3 [QA]** /voice persistence + voice-reply path tests. Spec:
      TV-005. **Deferred with T3.2.**

## Phase 4 — Docs

- [x] **T4.1 [DOCS]** README channels section + CHANGELOG entry.
- [x] **T4.2 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...` (fake binaries only — CI needs no real whisper/piper).
      Owner live acceptance on Telegram remains (echo/stop path, degraded
      hint) before issue #19 closes.
