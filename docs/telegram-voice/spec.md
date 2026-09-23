# Spec: Telegram voice messages (in: whisper.cpp, out: optional Piper) (#19)

> Decisions [D1]–[D5] settled 2026-09-23, taking the recommendations:
> STT shipped first with the `Synthesizer` seam ([D1]); default model
> `base-q5_1` ([D2]), models auto-download on first use ([D3]),
> `/voice off|auto|on` semantics ([D4]), audio never persisted ([D5]).
> The TTS transport wiring (TV-004) landed in the same feature arc —
> voice replies stream-suppressed + spoken at finalize with a text
> fallback.

Voice notes in, voice notes out — fully local, no cloud STT/TTS. Inbound:
OGG/Opus → ffmpeg → whisper.cpp (`whisper-cli`) → transcript → ordinary
prompt (with an echo-verification message). Outbound (optional): final
answer → Piper → WAV → ffmpeg → OGG/Opus voice note. Governing issue:
amurru/hakase#19.

## Ecosystem state (research, 2026-09-23)

- **whisper.cpp**: lives at `ggml-org/whisper.cpp` (MIT, active). Primary
  binary `whisper-cli` (CMake build); input must be 16-bit WAV
  (16 kHz mono via `ffmpeg -ar 16000 -ac 1 -c:a pcm_s16le`); models are
  ggml files from HuggingFace `ggerganov/whisper.cpp` (tiny 75 MiB → large
  2.9 GiB, quantized variants like `base-q5_1`); `-l` sets/`auto`-detects
  language; text output via `-otxt -of <base>`.
- **Piper**: the C++ `rhasspy/piper` was **archived 2025-10-06**; the
  maintained successor is the **`piper-tts` Python package**
  (OHF-Voice/piper1-gpl, GPL-3.0, v1.8.0 released 2026-09-04, Home
  Assistant maintainers, abi3 wheels for Linux/macOS/Windows). Voice models
  remain downloadable from HuggingFace `rhasspy/piper-voices` (MIT, live).
  The `piper` console command synthesizes to WAV from text.
- **Telegram Bot API**: voice notes are OGG/Opus, ≤ 20 MB via `getFile`
  (well above typical voice notes); `sendVoice` requires OGG/Opus — hence
  ffmpeg in the outbound direction too (WAV → libopus).

## Specs

### Spec TV-001: `internal/speech` — transport-neutral pipeline

New leaf package mirroring `internal/vision`'s optional-binary posture
(`vision.go:917` LookPath + degrade):

- `Transcriber` interface: `Transcribe(ctx, audio []byte, mime string,
  durationSec int) (string, error)`.
  `whisperCLI` implementation: temp dir → write input → `ffmpeg -i in -ar
  16000 -ac 1 -c:a pcm_s16le out.wav` → `whisper-cli -f out.wav -m <model>
  [-l <lang>] -otxt -of <base> -np` → read `.txt` → cleanup. Timeout
  (config) cancels via context.
- `Synthesizer` interface: `Synthesize(ctx, text string) ([]byte, error)`
  returning OGG/Opus: `piper --model <voice> --output_file out.wav` (text
  via stdin) → `ffmpeg -i out.wav -c:a libopus -b:a 32k out.ogg`.
- `Availability() error` for each: `exec.LookPath` on whisper-cli/ffmpeg/
  piper + model/voice file presence, error message names exactly what to
  install (acceptance criterion 2).
- **Model auto-download** on first transcription when the ggml file is
  missing: fetch `https://huggingface.co/ggerganov/whisper.cpp/resolve/main/
  ggml-<model>.bin` into `~/.hakase/models/whisper/` (0700 dir, 0600 file,
  atomic tmp+rename, size sanity check, logged). The URL base is a config
  override for offline/mirrored setups. **This is the only outbound network
  touch in the feature**; audio bytes never leave the machine (acceptance
  criterion 3) — documented and asserted by a test asserting the pipeline
  reads only local files after download.
- **Serialization**: one transcription at a time per process (CPU-bound),
  queue depth 3; overflow returns a typed `ErrBusy` so the transport can
  say "try in a moment" (whisperbot pattern).
- Test seams: binary paths are config fields; tests inject fake `whisper-cli`
  /`ffmpeg`/`piper` shell scripts via a temp PATH, and the download base URL
  is injectable (httptest).

### Spec TV-002: config

```json
"channels": { "telegram": {
  "speech_to_text": { "enabled": false, "model": "base-q5_1",
    "language": "auto", "binary_path": "whisper-cli", "ffmpeg_path": "ffmpeg",
    "models_dir": "", "max_seconds": 120, "timeout_seconds": 180 },
  "text_to_speech": { "enabled": false, "binary_path": "piper",
    "voices": { "default": "", "de": "" }, "ffmpeg_path": "ffmpeg", "max_chars": 1200 }
}}
```

- Both default **disabled** (issue: disabled by default). STT enabled with
  missing binaries → per-message actionable hint, never a run failure.
- `models_dir` empty → `~/.hakase/models/whisper/` (`HakaseHome`).
- `Validate()` on the sub-blocks (positive max_seconds/timeout/max_chars,
  model name charset). Env: `HAKASE_TELEGRAM_STT_ENABLED`,
  `HAKASE_TELEGRAM_TTS_ENABLED` minimum (strict bool policy).
- `enabled` tri-state `*bool` pattern (channels idiom).

### Spec TV-003: inbound voice on Telegram

- `handleMessage` (`telegram.go:348`): add a `m.Voice != nil` branch beside
  the photo branch. Guard: `m.Voice.Duration > max_seconds` → polite refuse.
- Download mirrors `photos.go` (`GetFile` + file URL GET, byte cap), then
  transcription through the serialized queue. `ErrBusy` → "🎙 queue is full,
  try again in a moment". Unavailable → actionable setup hint (names the
  config block + what to install).
- **Echo-verification**: send `🎙 Heard: <transcript>` (HTML-escaped,
  truncated preview) BEFORE starting the run — the user can `/stop` once
  they see a mis-transcription (issue step 4). No confirmation gate.
- The transcript becomes the prompt text through the normal `startRun` →
  `RecordUsageInSession` → `agentrun.Driver` path (pairing/auth unchanged);
  the original audio is NOT persisted (privacy) — the session shows the
  transcript like any text turn.
- `api` interface gains `SendVoice`; `fakeAPI` extended (voice sends
  recorded for assertions).

### Spec TV-004: outbound voice (`/voice` modes)

- Per-chat preference `state.Chat.VoiceMode` (`off` default; `auto`; `on`),
  persisted via `store.Update` like the notify toggle
  (`commands.go:383-415`); `/voice` command in the command table + help.
  - `off` (default): text replies only.
  - `auto`: voice reply **when the turn arrived as a voice note**.
  - `on`: voice replies always.
- TTS runs only when TTS is enabled AND the mode calls for it AND the
  final answer is non-empty: strip markdown (reuse the telegram
  send-path's plain-text fallback logic), cap at `max_chars` with an
  explicit "[truncated for voice]" marker, synthesize, `SendVoice` with
  `MessageThreadID` and a short caption. The full text still exists in the
  session/web transcript; the voice note replaces the big text message on
  Telegram. Synthesis failure degrades to the normal text send (never
  loses the answer).

### Spec TV-005: tests

- speech: pipeline happy path via fake binaries; missing binary →
  Availability error naming it; busy queue; model download (httptest) +
  atomic install; no-network-after-model assertion; timeout.
- telegram: voice → echo → run-with-transcript (fake driver); refuse on
  duration; degraded hint; `/voice` persistence; voice reply path when
  enabled+auto+voice-inbound.

## Decision points (discuss before implementation)

- **[D1] TTS engine**: `piper-tts` (maintained Python CLI, GPL — a Python
  3.9+ runtime dependency on the host) vs the archived C++ binary (frozen,
  no new deps) vs defer TTS entirely and ship STT-only first. *Recommendation:
  implement the `Synthesizer` seam now, wire `piper-tts`, but treat TTS as
  the stretch goal — STT alone satisfies all three acceptance criteria.*
- **[D2] Default whisper model**: `base-q5_1` (~58 MiB, multilingual,
  auto-detect) vs `small-q5_1` (better accuracy, ~182 MiB) vs asking the
  user to choose at setup. *Recommendation: `base-q5_1`, config-overridable,
  auto-downloaded on first use.*
- **[D3] Model auto-download**: automatic on first use (issue's step 3)
  vs install-manually-only. *Recommendation: auto-download with the config
  URL override for offline setups.*
- **[D4] Voice-reply semantics**: the `off|auto|on` mode set above vs the
  issue's `only|on|off` phrasing. *Recommendation: `off|auto|on` — `auto`
  is the natural daily mode (you talk, it talks back).*
- **[D5] Persist the audio?** Current plan: never persist audio (transcript
  only) — keeps sessions/privacy simple. Alternative: keep the OGG as a
  session attachment for replay. *Recommendation: don't persist (v1).*
