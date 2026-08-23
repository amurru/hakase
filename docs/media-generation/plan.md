# Execution Plan: Pluggable Media Generation

Feature: `media-generation`
Source of truth: `spec.md` (atomic specs MG-001..MG-011, r2). This file sequences the work into phases with owners, exit criteria, and parallelization.
Date: 2026-08-21 (r3). Scope change from r1: **ComfyUI deferred to v2**; v1 = `pil` + `openai` + `fal`. r3: fal video slug pinned; dall-e-3 marked EOL (official API shutdown 2026-05-12); live captures split - OpenRouter runnable now, official-OpenAI/fal deferred until accounts exist.

## Phases

### Phase 0 - Decisions (CLOSED r2)

- [x] **T0.1** `auto` order is `["openai","fal","pil"]`; image `auto` always succeeds via pil; video/audio `auto` errors actionably when unconfigured. Recorded in `research.md`.
- [x] **T0.2** PIL path: **Go-native** (`image/draw` + embedded gofont), with an isolated optional shaping layer (`textlayout.go`, `go-text/typesetting`) for Arabic/CJK.
- [x] **T0.3** ComfyUI deferred to v2 (untestable without GPU hardware + local models); key conventions are house-pattern only (`api_key` fallback, `HAKASE_*` env); OpenRouter supported via `openai_image_path: "/images"`.

Exit: recorded in `research.md` and `spec.md`. Done.

### Phase 1 - Foundation (serial)

**MG-001** Config block and env overrides (`internal/config/config.go`).
- Exit: `MediaConfig` exists with defaults, validation, env precedence (`HAKASE_MEDIA_*`, `HAKASE_FAL_KEY`), zero-config regression test green, `config.json.example` documented, `go test ./internal/config/...` green.

**MG-003** Sandbox-confined media store (`internal/media/store.go`).
- Exit: `Store` allocates `<ulid><ext>` under `outputs/media` via `securejoin` + atomic capped write; traversal tests pass.

**MG-002** Provider interface and registry (`internal/media/provider.go` + `registry.go`).
- Exit: `Provider` interface + `Registry.Resolve("image")` walks `order`, key-presence health, per-provider semaphore, factory map built inside `NewRegistry` (no global registration), all unit tests green.

Sequence: MG-001 -> MG-003 -> MG-002.

### Phase 2 - Parallel tracks (after MG-002; MG-010 can start after MG-001+002)

Run these four tracks concurrently:

- **Track A (BE): MG-004** Go-native fallback provider (`pil.go` + optional `textlayout.go`). Exit: valid PNG at requested size, prompt-required error, layout interface in place.
- **Track B (BE): MG-006** OpenAI Images provider (`openai.go`). Exit: mock-server tests for b64 success / 401 / 404-path-hint / per-model clamping / path override.
- **Track C (BE): MG-007** fal.ai provider (`fal.go`). Exit: queue+poll+download mocks, SSRF rejection test, pinned video slug recorded in `fixtures.md`.
- **Track D (BE+FE): MG-010** Settings UI + `GET /api/media/status` (`internal/web/handlers/media.go` + `SettingsView.vue`). Exit: status auth + redaction tests, UI section builds, `pnpm test` green.

Exit per track: the track's acceptance criteria + its slice of `go test ./...` / `pnpm test` green.

### Phase 3 - Integration (after A+B+C)

**MG-008** ADK tools (`internal/media/tools.go`).
- Exit: `generate_image` via pil succeeds end-to-end, `generate_video`/`generate_audio`/off error strings verbatim, size clamping works, mutex-guarded manifest appends under concurrency, semaphore released on timeout, `go test ./internal/media/...` green.

**MG-009** Agent wiring (`cmd/hakase/main.go` + `internal/agent/deps.go` + `agent.go`).
- Exit: registry constructed unconditionally, `CreateMediaToolsFn` bridge wired, `go build ./...` green, instruction contains `### MEDIA GENERATION:`, tool count includes 3 media tools with default config.

### Phase 4 - Hardening (after integration)

**MG-011** Docs, skills, and support matrix.
- Exit: `fixtures.md` captures payloads incl. one live capture per cloud provider with an available account (OpenRouter at merge time; official-OpenAI and fal deferred until accounts exist), `.agents/skills/baoyu-infographic` updated, README TODO closed with ComfyUI-deferred note, `support.md` published.

## Critical Path

```text
MG-001 ─► MG-003 ─► MG-002 ─┬─► MG-004 ─┐
                            ├─► MG-006 ─┼─► MG-008 ─► MG-009 ─► MG-011
                            └─► MG-007 ─┘         ▲
MG-010 (parallel after MG-001+002) ──────────────┘
```

- MG-001 is the sole serial prerequisite; MG-002 is the fan-out point.
- MG-008 is the integration gate before wiring.
- MG-010 is off-critical-path.

## Suggested Task Sizing (1-3 tool calls each)

See `tasks.md` for the atomic task list:

```bash
go test ./internal/config/...        # MG-001
go test ./internal/media/...         # MG-002..MG-008
go build ./...                       # MG-009
cd webui && pnpm test                # MG-010
```

## Definition of Done (feature-level)

1. `generate_image` with `{"prompt":"a poster for baoyu infographic about Tokyo transit"}` succeeds on a fresh clone with zero config (via pil) and writes `outputs/media/<ulid>.png` that renders inline in chat via existing `mediaLinks` + `/api/files/inline`, no console errors, no CSP violations.
2. With `HAKASE_FAL_KEY` set, or an OpenAI/OpenRouter key resolvable through `api_key`/`openai_image_key`, the same call picks the higher-priority provider (verified via `GET /api/media/status` `resolved_image`); against OpenRouter this requires `openai_image_path: "/images"`.
3. `generate_video` with no provider returns the actionable error verbatim; with `fal_key` set returns `.mp4` via the same pipeline. `generate_audio` returns its stub message.
4. `go test ./...` green (`internal/config`, `internal/media`, `internal/agent`, `internal/web`) and `pnpm test` green.
5. `make build` produces a binary whose embedded SPA includes the Media section in SettingsView and exposes no raw keys via `/api/media/status`.
6. `fixtures.md` captures verified before/after for pil/openai/fal including live captures - **OpenRouter capture first (account available); official-OpenAI and fal captures deferred until accounts exist**; `outputs/media/manifest.jsonl` appends under concurrent generation.
7. README TODO updated: cloud + fallback shipped, ComfyUI explicitly deferred to v2.

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| OpenRouter/OpenAI path mismatch silently 404s | Medium | Medium | `openai_image_path` override + 404 error hint names the fix; fixture covers both paths |
| Wrong size enum for chosen model (400) | Medium | Low | Per-model clamp table in MG-006; unknown slugs pass through |
| fal output host drift breaks downloads | Medium | Medium | Shared SSRF-guard fetch (no host allowlist) mirrors `/api/files/proxy` |
| fal video slug churn | Medium | Low | Pinned r3 to `fal-ai/wan/v2.7/text-to-video` (schema-verified); config-overridable |
| GPT Image org verification missing | Low | Medium | Confirmed current requirement (r3): verify org, up to 30-min propagation, fresh API key; **OpenRouter bypasses entirely** - default testing path unaffected |
| dall-e-3 references break (retired 2026-05-12) | Low | Low | Clamp rule kept only for router aliases; default model is `gpt-image-1-mini`; fixtures mark legacy rows |
| Go text shaping regressions (Arabic/CJK) | Medium | Low | Isolated `textlayout.go` behind interface; graceful fallback to gofont; may slip to v1.1 without blocking |
| Semaphore starvation | Low | Medium | Per-provider semaphore + actionable busy behavior |
| Key logged in audit/manifest/status | Low | High | Redact + tests assert no key anywhere |
| Sandbox traversal via provider filename | Low | High | Store ignores provider filenames (ULID only) + securejoin, tested with `..` |
| Cloud cost surprise | Medium | Medium | Honest framing: `auto` tries cloud before pil whenever a key resolves; provider+model surfaced in every tool result and status endpoint; hard cost guard deferred to v2 |
| Agent blocks on video (60-180s) | Medium | Medium | `context.WithTimeout(300s)` resolved in tool layer; timeout releases semaphore |
