# Task List: Pluggable Media Generation

Feature: `media-generation` (r3 scope: `pil` + `openai` + `fal`; ComfyUI deferred to v2; fal video slug pinned; dall-e-3 EOL)
Atomic, hand-offable tasks. Each references the governing spec in `spec.md` and is sized to 1-3 tool calls + one verification step.
Date: 2026-08-21 (r3)

Legend: `[BE]` Go backend, `[FE]` frontend, `[QA]` test/docs. Status: TODO unless marked.

---

## Phase 0 - Decisions (CLOSED)

- [x] **T0.1 [QA]** `auto` order = `["openai","fal","pil"]`; pil guarantee for image; video/audio error actionably when unconfigured. Recorded in `research.md`. Governs MG-001, MG-002.
- [x] **T0.2 [QA]** PIL path: Go-native; embedded gofont required; optional shaping layer (`textlayout.go`, `go-text/typesetting`) isolated behind a `textLayout` interface. Governs MG-004.
- [x] **T0.3 [QA]** ComfyUI deferred to v2 (design preserved in spec.md "Deferred to v2"). Key conventions: house-pattern only (`api_key` fallback chain, `HAKASE_*` env). OpenRouter via `openai_image_path`. Governs MG-001, MG-005, MG-006.

---

## Phase 1 - Foundation

- [ ] **T1.1 [BE]** Add `MediaConfig` to `internal/config/config.go`: fields per MG-001 contract (incl. `OpenAIImagePath`, `OpenAIImageModel`, `FalImageModel`, `FalVideoModel`; no ComfyUI fields), `ApplyDefaults()`, `Validate()`, redaction in any debug rendering.
      Verify: `go test ./internal/config/...` defaults/validation + zero-config regression case. Spec: MG-001.

- [ ] **T1.2 [BE]** Env overrides in `LoadConfig(filePath string)` after file read: `HAKASE_MEDIA_IMAGE_PROVIDER`, `HAKASE_MEDIA_VIDEO_PROVIDER`, `HAKASE_MEDIA_OUTPUT_DIR`, `HAKASE_FAL_KEY`, `HAKASE_MEDIA_VIDEO_MODEL`. Nothing else - do NOT add `OPENAI_API_KEY`/`FAL_KEY`/`REPLICATE_API_TOKEN`.
      Verify: `go test ./internal/config/...` precedence file < env. Spec: MG-001.

- [ ] **T1.3 [QA]** Update `config.json.example` with documented `media` block: commented defaults, env hints, and an OpenRouter example (`base_url` + `"openai_image_path": "/images"`).
      Verify: manual read / `jq .media config.json.example`. Spec: MG-001.

- [ ] **T1.4 [BE]** Create `internal/media/store.go`: `NewStore(outputDir string)` resolving through `sandbox.CurrentSandbox` else `filepath.Abs`; `Allocate(ext)` with ULID + `securejoin` + `EvalSymlinks` re-check + extension whitelist; `Write(path, r, maxBytes)` atomic capped write `0644`.
      Verify: `go test ./internal/media/...` traversal rejection, atomic write, size cap, dir creation. Spec: MG-003.

- [ ] **T1.5 [BE]** Create `internal/media/provider.go`: `Provider` interface, `Capabilities`, request/result types, validation helpers (prompt 1-4000, clamp 256-2048).
      Verify: `go test ./internal/media/...` validation cases. Spec: MG-002.

- [ ] **T1.6 [BE]** Create `internal/media/registry.go`: `NewRegistry(cfg, log, store)` builds the factory map internally (**no package-level `Register`**), `Resolve(kind)` walks `Order` with key-presence health (pil always healthy; cloud healthy iff key resolves), `Get(name)`, per-provider semaphore `Acquire/Release`.
      Verify: `go test ./internal/media/...` auto order, unhealthy skip, semaphore blocking, explicit-missing error, parallel-safe construction. Spec: MG-002.

---

## Phase 2 - Parallel provider tracks (after T1.6; Track D can start after T1.1+T1.6)

### Track A - Go-native fallback (MG-004)

- [ ] **T2A.1 [BE]** Create `internal/media/pil.go`: `Name()="pil"`, `Capabilities{Image:true}`, rendering background/rounded card/title/shapes deterministic in prompt+seed via `image/draw` + embedded `goregular`. Define the `textLayout` interface here; wire `simpleLayout`.
      Verify: valid PNG at requested size, prompt-required error, determinism smoke test. Spec: MG-004.

- [ ] **T2A.2 [BE]** *(included, may slip to v1.1 without blocking)* Create `internal/media/textlayout.go`: system-font discovery + `go-text/typesetting` shaping (Arabic/CJK/Indic), cmap coverage selection, fallback to `simpleLayout`. Adds direct dep to `go.mod`. **Determinism contract**: font selection must be deterministic for a given prompt+seed - embedded `goregular` is tried first, discovered system fonts are probed in a fixed priority order (no host-dependent tiebreaks), and the chosen-font decision must be reproducible on any host with the same fonts installed. The T2A.1 determinism guarantee applies to hosts without covering system fonts; cross-host pixel equality is only claimed when selection resolves to the embedded font.
      Verify: coverage-selection unit tests; Arabic/CJK title renders non-tofu when a covering system font exists; deterministic font-selection unit test. Spec: MG-004.

### Track B - OpenAI Images (MG-006)

- [ ] **T2B.1 [BE]** Create `internal/media/openai.go`: plain `net/http`, endpoint `(openai_image_base_url || cfg.BaseURL || official) + (openai_image_path || "/images/generations")`, bearer `(openai_image_key || cfg.APIKey)`, body `{model, prompt, n:1, size}` with `response_format` only for `dall-e-*`, per-model clamp table (gpt-image-2 arbitrary-legal / gpt-image-1 trio / dall-e-3 trio / unknown pass-through), b64 decode -> Store, actionable 401 and 404-with-path-hint errors.
      Verify: httptest mocks - b64 success, 401, 404 hint, clamping per model, path override, response_format gating. Spec: MG-006.

### Track C - fal.ai (MG-007)

- [ ] **T2C.1 [BE]** Create `internal/media/fal.go`: queue POST + status poll (1s) + download via the shared SSRF-guard fetch pattern (https, public-IP dial validation, redirect re-check, `io.CopyN` caps) - no host allowlist. Image default `fal-ai/flux/schnell`; video default pinned r3: `fal-ai/wan/v2.7/text-to-video` with the MG-007 mapping (resolution `"720p"`, aspect_ratio nearest-match from width:height, duration clamp 2-10, seed pass-through).
      Verify: httptest mocks - queue+poll+download, 401, poll timeout, SSRF rejection of private-IP result URLs, wan mapping unit tests. Spec: MG-007.

### Track D - Settings UI + status endpoint (MG-010)

- [ ] **T2D.1 [BE]** Create `internal/web/handlers/media.go`: `GET /api/media/status` (resolved providers, capabilities with `configured` booleans, no keys, no network probes) + optional `GET /api/media/manifest` (last 20 lines). Register inside the auth group in `internal/web/server.go`.
      Verify: `go test ./internal/web/...` auth, resolved fields, no key leak. Spec: MG-010.

- [ ] **T2D.2 [FE]** Extend `webui/src/views/SettingsView.vue` with the Media section: provider selects, password inputs (`fal_key`, `openai_image_key`, `openai_video_key`), text inputs (`openai_image_base_url`, `openai_image_path`, `openai_image_model`, `openai_video_base_url`, `openai_video_model`, `fal_base_url`, `fal_image_model`, `fal_video_model`, `order`), numbers (`max_concurrent`, `timeout_seconds`), video resolution select (`openai_video_resolution`). Resolved badges + "pil always available" hint via `getMediaStatus()`; reuse `has_*_key` boolean pattern.
      Verify: `cd webui && pnpm build && pnpm test`. Spec: MG-010.

- [ ] **T2D.4 [BE+FE]** *(r4)* OpenAI-compatible video configuration flow: write-only `openai_video_key` / `clear_openai_video_key` control keys (type validation, clear-over-set precedence, nested-secret stripping), Settings UI video key card + base URL/model/resolution fields wired into load/save.
      Verify: handler lifecycle test (set -> redacted GET -> clear precedence -> clear-only), malformed-control 400s; `pnpm build`. Spec: MG-008 r4.

- [ ] **T2D.3 [FE]** Add `getMediaStatus()` / `getMediaManifest()` to `webui/src/lib/api.ts`.
      Verify: `curl -H "Authorization: Bearer $JWT" http://localhost:8080/api/media/status | jq`. Spec: MG-010.

---

## Phase 3 - Integration (after Tracks A+B+C)

- [ ] **T3.1 [BE]** Create `internal/media/tools.go` via `util.NewDocTool`: `generate_image` / `generate_video` / `generate_audio` per MG-008 input/output contracts. Clamp sizes, resolve provider, acquire semaphore, `context.WithTimeout` resolved in the tool layer (120s image / 300s video), mutex-guarded single-line manifest appends, verbatim error strings for video/audio/off states, markdown snippet in output.
      Verify: `go test ./internal/media/...` pil success, verbatim error strings, clamping, provider override, concurrent manifest appends, timeout releases semaphore. Spec: MG-008.

- [ ] **T3.2 [BE]** Wire the harness: add `CreateMediaToolsFn func(log interfaces.LogFunc) ([]tool.Tool, error)` to `agent.Deps` (agent does NOT import internal/media); construct registry unconditionally in `cmd/hakase/main.go` (WARN-and-continue on construction failure); append tools in `setupRunner`; add `### MEDIA GENERATION:` section after `### KNOWLEDGE BASE:` in `buildOrchestratorInstruction`.
      Verify: `go build ./...`; `go test ./internal/agent/...` tool count + instruction contains MEDIA section + nil-factory safe + zero-config end-to-end regression. Spec: MG-009.

- [ ] **T3.3 [QA]** Manual integration: fresh clone (no keys) -> `generate_image` via pil renders inline in chat. Set `HAKASE_FAL_KEY` (or configure OpenRouter path) -> `resolved_image` switches and the same call uses the higher-priority provider.
      Verify: manual run + `GET /api/media/status`. Spec: MG-008+MG-009.

---

## Phase 4 - Hardening (MG-011)

- [ ] **T4.1 [QA]** Complete `docs/media-generation/fixtures.md`: mock payloads for all providers plus live captures, **in this order**:
      1. OpenRouter image capture - runnable now with the existing account (~$0.01-0.05 via e.g. `bytedance-seed/seedream-4.5`).
      2. pil capture - free, any time.
      3. Official-OpenAI and fal captures - **deferred until accounts exist** (fal image ~$0.003; fal video on the pinned wan-v2.7 slug ~$0.50 for 5s @720p).
      Record request body sent, response (redacted/truncated), manifest line, and inline-render screenshot per capture.
      Verify: `media_test.go` covers mock payloads only. Live captures require manual sign-off - confirm the capture order above and that each recorded artifact (request body, redacted response, manifest line, inline-render screenshot) exists in this file. Spec: MG-011.

- [ ] **T4.2 [QA]** Update `.agents/skills/baoyu-infographic/SKILL.md`: prefer `generate_image` (prompt orchestration, `provider:"auto"`), fall back to HTML/SVG when unavailable. Do not modify the comfyui skill in v1.
      Verify: `hakase skill validate .agents/skills/baoyu-infographic` or `go test ./internal/skill/...`. Spec: MG-011.

- [ ] **T4.3 [QA]** Update `README.md`: close the `image_gen`/`video_gen` TODO (note ComfyUI deferred to v2), add media bullet to Features, document the `media` block and `HAKASE_MEDIA_*` / `HAKASE_FAL_KEY` env vars.
      Verify: `grep -n "media\|image_gen" README.md`. Spec: MG-011.

- [ ] **T4.4 [QA]** Finalize `docs/media-generation/support.md`: provider matrix, env var table, troubleshooting (401, 404 path hint for OpenRouter, GPT Image org verification, size caps, cost note, SSRF behavior), rollout notes.
      Verify: linked from README and fixtures. Spec: MG-011.

---

## Verification Commands

```bash
# Backend (every task)
go test ./internal/config/...        # after T1.1-T1.2
go test ./internal/media/...         # after T1.4-T3.1
go test ./internal/agent/...         # after T3.2
go test ./internal/web/...           # after T2D.1
go test ./...                        # full suite before merge
go build ./...                       # wiring check after T3.2

# Frontend
cd webui
pnpm install
pnpm build            # typecheck included; check no key material in bundle
pnpm test             # after T2D.2

# Full build (embed)
make build

# Manual QA (after T3.3)
go run -tags dev ./cmd/hakase/ web --port 8080
# Chat: "generate a poster for Tokyo transit infographic" -> outputs/media/<ulid>.png renders inline
# curl -H "Authorization: Bearer $JWT" http://127.0.0.1:8080/api/media/status | jq
# curl -H "Authorization: Bearer $JWT" "http://127.0.0.1:8080/api/files/inline?path=outputs/media/<ulid>.png" -o /tmp/test.png
```

## QA Matrix

| Payload | Expected |
|---|---|
| `generate_image({"prompt":"poster about X"})` zero config | pil PNG at requested size in `outputs/media/<ulid>.png`, `provider:"pil"`, renders via `mediaLinks` + `/api/files/inline`, no CSP error |
| `generate_image({"prompt":"...","width":512,"height":512,"provider":"pil"})` | clamped, explicit provider used |
| `generate_image` with `HAKASE_FAL_KEY` set, `auto` | fal chosen (`resolved_image:"fal"`), mocked in tests |
| `generate_image` via OpenRouter config (`openai_image_path:"/images"`) | POST hits `{base}/images`, b64 decoded, `provider:"openai"` |
| `generate_image({"model":"dall-e-3","width":2000,"height":1000})` | clamped to `1792x1024`, `response_format:"b64_json"` sent |
| `generate_image({"model":"gpt-image-1-mini", ...})` | no `response_format` field in request body |
| `generate_image({"provider":"openai"})` with bad key | `openai image auth failed: check api_key / openai_image_key (401)` |
| Images endpoint 404 (wrong path) | error hints `for OpenRouter set openai_image_path to "/images"` |
| `generate_video({"duration_seconds":5})` no provider | verbatim requires-provider error |
| `generate_video` with `fal_key` | `.mp4` in store, renders as `<video controls>` |
| `generate_audio({"text":"hello"})` off | verbatim off message |
| `image_provider:"off"` + generate_image | verbatim off message |
| `GET /api/media/status` without auth | 401 |
| `GET /api/media/status` with auth | resolved fields, `configured` booleans, zero raw keys |
| `Store.Allocate("../evil.png")` attempt | rejected via securejoin |
| fal result URL pointing at private IP | rejected by SSRF guard |
| Slow provider exceeding timeout | `context.DeadlineExceeded` as tool error, semaphore released |
| Concurrent generations | manifest.jsonl lines intact (mutex, one write per line) |
