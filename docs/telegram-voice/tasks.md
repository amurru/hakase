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
- [x] **T2.5 [BE/QA]** attached media files (audio/video/document) go to
      the model as native inline parts with the caption as prompt — only
      voice NOTES are transcribed (spec decision + directive); the old
      silent drop / caption loss for attached files is fixed. Tests:
      file_test.go.

## Phase 3 — Outbound TTS

- [x] **T3.1 [BE]** Synthesizer seam (PiperTTS: piper → WAV → OGG) with
      fake-binary tests. Spec: TV-001/004.
- [x] **T3.2 [BE]** `state.Chat.VoiceMode` + `/voice off|auto|on` command +
      menu/help + finalizer wiring (stream suppression in voice mode,
      markdown strip, max_chars truncate, SendVoice, text fallback on any
      synthesis failure; rebinds preserve per-chat prefs). Spec: TV-004.
- [x] **T3.3 [QA]** /voice persistence + mode semantics + fallback tests
      (fakeSynthesizer/streamingDriver). Spec: TV-005.
- [x] **T3.4 [BE/QA]** Multilingual mirroring: whisper's detected language
      (`-oj` JSON) rides through the turn; `text_to_speech.voices` (reserved
      `"default"` + per-language entries) selects the voice; unconfigured
      languages, typed prompts, and missing voice files all fall back to
      `"default"` (tested at both layers).

## Phase 4 — Docs

- [x] **T4.1 [DOCS]** README channels section + DEVELOPMENT.md + CHANGELOG.
- [x] **T4.2 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...` (fake binaries only — CI needs no real whisper/piper).
      Owner live acceptance on Telegram remains (echo/stop path, degraded
      hint, voice reply) before issue #19 closes.

## Deferred

- Web UI voice input: microphone capture in the chat composer feeding this
  same pipeline (`internal/speech` is transport-neutral) — recorded in the
  ROADMAP deferred ledger and CHANGELOG Planned.
- Arabic-script voice quality note: piper voices are per-language; a
  reply whose language has no configured voice entry speaks with the
  default voice (script detection covers the configured set).
