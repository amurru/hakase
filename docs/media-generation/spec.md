# Development Blueprint: Pluggable Media Generation

Feature: `media-generation`
Status: Spec ready for execution. See `plan.md` (sequence) and `tasks.md` (atomic tasks).
Revision: r2, 2026-08-21. Changes from r1: **ComfyUI deferred to v2** (design preserved under "Deferred to v2"); v1 ships `pil` + `openai` + `fal`. Key handling aligned with house patterns (`api_key` fallback, `HAKASE_*` env vars only - no `OPENAI_API_KEY`). MG-006 rewritten for plain `net/http` with an `openai_image_path` override (OpenRouter compatibility) and per-model size rules. Registry construction is unconditional (zero-config guarantee). Bridge-factory wiring per AGENTS.md.
Revision: r3, 2026-08-21. Changes from r2: fal video slug pinned (`fal-ai/wan/v2.7/text-to-video`, schema + pricing verified); dall-e-3 marked EOL on the official API (shutdown 2026-05-12, rule kept for router aliases); GPT Image org-verification guidance confirmed current (30-min propagation, new key afterwards; not applicable via OpenRouter).
Revision: r4, 2026-08-22. Changes from r3: **OpenAI-compatible video generation added to the contract** (`openai_video_key` / `openai_video_base_url` / `openai_video_model` / `openai_video_resolution`, OpenRouter-style async jobs API with `polling_url` + `unsigned_urls`, image-to-video via first-frame `frame_images`, `HAKASE_MEDIA_VIDEO_MODEL` env override); `video_provider` gains `openai`; MG-008 `generate_video` input/error strings updated to the shipped implementation; MG-011 live-capture gate made account-availability aware (OpenRouter required at merge; official-OpenAI/fal deferred until accounts exist).
Scope: Go backend (`internal/media`, `internal/config`, `internal/agent`, `internal/web`) + minimal frontend (`webui/src/views/SettingsView.vue`).

## Context and Objective

Hakase's README TODO has been open since the creative skills port: native `image_gen` and `video_gen` tools do not exist. Three shipped skills are degraded:

- `baoyu-infographic` renders HTML/SVG instead of images
- `comfyui` is doctrine + requires manual `comfy-cli` setup with no harness integration (v1 does not change this; revisit in v2)
- `songwriting-and-ai-music` shells to external Suno, no native audio

Objective: add `generate_image`, `generate_video` (and stub `generate_audio`) as first-class ADK tools backed by a **provider-agnostic, pluggable layer** that:

1. Works offline with zero config (PIL-equivalent Go-native fallback for images).
2. Uses cloud quality when keys are present (OpenAI images; OpenAI-compatible endpoints such as OpenRouter via `base_url` + path override; fal.ai for images and video) - configured via one `media` block plus `HAKASE_*` env vars.
3. Never blocks the agent loop beyond a bounded timeout, never leaks keys, never writes outside `outputs/media/`, and is observable via audit logs and a manifest.

All rendering reuses the existing markdown media pipeline (`mediaLinks` plugin + `/api/files/inline`) so no new viewer is needed.

## Repository Dependency Map

| Path | Role | Dependencies / Touchpoints | Risk |
|---|---|---|---|
| `internal/media/` (new) | Provider interface, registry, store, PIL-equivalent + HTTP providers | `internal/config`, `internal/sandbox`, `internal/util`, `net/http`, `image/*` | High (new) |
| `internal/media/provider.go` (new) | `Provider` interface + `Capabilities` + request/result types | none | Low |
| `internal/media/registry.go` (new) | Priority-order `auto` resolution + `MaxConcurrent` semaphore + key-presence health | `internal/media/provider.go`, `internal/config` | Medium |
| `internal/media/store.go` (new) | Sandbox-confined `outputs/media` allocator | `internal/sandbox`, `cyphar/filepath-securejoin` | Medium |
| `internal/media/pil.go` (new) | Always-available image provider via Go `image/draw` + `golang.org/x/image` | `internal/media/store.go`, `golang.org/x/image/font/gofont` | Low |
| `internal/media/textlayout.go` (new, optional) | System-font discovery + shaping (Arabic/CJK) via `go-text/typesetting`; degrades to embedded Latin font | `github.com/go-text/typesetting` (new direct dep) | Medium |
| `internal/media/openai.go` (new) | OpenAI Images provider (plain `net/http`, OpenAI-compatible endpoints incl. OpenRouter) | `net/http`, `internal/media/store.go` | Medium |
| `internal/media/fal.go` (new) | fal.ai provider (queue + poll + SSRF-guarded download) | `net/http`, `internal/media/store.go` | Medium |
| `internal/media/tools.go` (new) | ADK tool factories `CreateMediaTools` | `google.golang.org/adk/v2`, `internal/util.NewDocTool`, `internal/media/registry.go` | High |
| `internal/config/config.go` | Add `MediaConfig` struct + env overrides + validation | `internal/sandbox` | Medium |
| `internal/agent/deps.go` | Add `CreateMediaToolsFn` bridge factory to `Deps` (house pattern; agent does NOT import `internal/media`) | none | Low |
| `cmd/hakase/main.go` | Construct registry unconditionally; wire `CreateMediaToolsFn` closure | `internal/media`, `internal/agent` | Low |
| `internal/agent/agent.go` | Append media tools via `deps.CreateMediaToolsFn`; add `### MEDIA GENERATION:` instruction section | `internal/agent/deps.go` | Medium |
| `internal/web/handlers/media.go` (new) | `GET /api/media/status`, `GET /api/media/manifest` (optional v1) | `internal/media` | Low |
| `internal/web/handlers/media_test.go` (new) | Status auth + redaction tests | - | Low |
| `webui/src/views/SettingsView.vue` | Media provider selectors + key inputs | `webui/src/lib/api.ts` | Medium |
| `webui/src/lib/api.ts` | `getMediaStatus`, `getMediaManifest` | - | Low |
| `config.json.example` | Document `media` block | - | Low |
| `.agents/skills/baoyu-infographic/SKILL.md` | Update to prefer `generate_image` when available | - | Low |

No other modules affected. MCP, knowledge, sessions, TUI unchanged. The `comfyui` skill is **not** modified in v1 (it stays doctrine-only; see Deferred to v2).

## Architectural Guardrails

### Allowed

- One `Provider` interface with `Capabilities()` negotiation and `auto` priority order. Providers are constructed from a **factory map built inside `NewRegistry`** - no package-level registration state.
- `MediaStore` that allocates paths via `securejoin` + `EvalSymlinks` and writes atomically (`tmp file + os.Rename`), mirroring `SaveNote` / `saveTaskRegistryLocked`.
- Plain `net/http` for both cloud providers (no SDK). The `openai` provider follows the existing `base + path` convention from `provider.go:GetModelInfo`, so OpenAI-compatible endpoints (OpenRouter, vLLM) work via `base_url` + `openai_image_path`.
- PIL-equivalent fallback in pure Go (`image/draw`, `golang.org/x/image/font/gofont` - already vendored). Optional shaping layer via `go-text/typesetting` (pure Go, no cgo) isolated in `textlayout.go`.
- Cloud downloads via the **existing SSRF-guarded fetch pattern** (`internal/web/handlers/file.go proxyHTTPClient`: https scheme, public-IP dial validation, redirect re-check per hop) plus `io.CopyN` size caps (20MB image, 100MB video). No hostname allowlist.
- Semaphore (buffered channel or `golang.org/x/sync/semaphore`) for `MaxConcurrent` - 1 for `pil`, 4 for cloud.

### Forbidden

- Per-provider tool files (e.g. `comfyui_tools.go`). One tool `generate_image` negotiates provider internally.
- Shelling to `comfy-cli`, `curl`, or any subprocess for generation. All HTTP via Go client, all local drawing in-process.
- Writing provider output to arbitrary paths. Must go through `MediaStore.Allocate`.
- Logging `api_key`, `fal_key`, `openai_image_key` in plaintext. Must redact like the vision config.
- Reading foreign env-var conventions (`OPENAI_API_KEY`, `REPLICATE_API_TOKEN`, `FAL_KEY`). Only `HAKASE_*` variables are honored (house rule; matches `HAKASE_VISION_API_KEY` precedent).
- Hardcoding provider request paths or model names without config override (both caused real integration failures in review).
- Adding `unsafe-eval` or new CSP directives. Generated media is always re-served same-origin via `/api/files/inline`; the browser never sees provider URLs, so CSP is untouched by design.
- Blocking the agent beyond `timeout_seconds`. Must use `context.WithTimeout` and return `context.DeadlineExceeded` as a tool error.
- Automatic retries on cloud calls (paid, non-idempotent - see Non-Goals).
- Bundling SD model weights, fonts beyond the embedded Go font, or large binaries.

### Constraints

- `outputs/media/` is the only writable media directory in v1. `OutputDir` must be sandbox-resolved.
- `pil` must never require network, pip packages, or cgo.
- `auto` for `generate_image` must always succeed (`pil` guarantee). `auto` for `generate_video`/`generate_audio` may fail with an actionable message when no provider is configured.
- Tool JSON schemas must carry `doc:` tags injected via `util.NewDocTool` (ADK requirement, same as `knowledge_tools.go`).
- Config `media` block must be optional - absent means defaults (`image_provider: "auto"` resolving to `pil`), no breakage for existing `config.json`. The registry is constructed **unconditionally** at startup.
- `internal/agent` must not import `internal/media`; wiring goes through a `Deps` bridge factory like knowledge tools and MCP.

### Non-Goals (explicit, so executors don't improvise)

1. **No automatic retries.** Cloud generation calls are paid and non-idempotent (a timed-out video may still have been generated and billed). Any 429/5xx/timeout surfaces as an actionable tool error including the HTTP status; the agent decides whether to call the tool again.
2. **One pinned fal video model.** A single default slug (recorded in `fixtures.md` after checking fal's current catalog/pricing) with one request-mapping implementation. The tool's `model` param passes through, but only the default's schema is tested. No per-model adapters.
3. **No per-model adapters for OpenAI-compatible endpoints either.** Size clamping follows the per-model table in MG-006; unknown slugs pass `size` through and let the endpoint clamp.
4. **No field-level merge of user-home config.** `~/.hakase/config.json` remains a whole-file fallback (used only when no project config exists), matching current loader behavior.

## Atomic Specs

### Spec MG-001: Config block and env overrides

- Objective: Add `MediaConfig` to `internal/config/config.go` with validation, defaults, and env var overrides. Document in `config.json.example`.
- Acceptance Criteria:
  - `MediaConfig` struct with fields: `ImageProvider`, `VideoProvider`, `AudioProvider`, `Order`, `MaxConcurrent`, `TimeoutSeconds`, `OutputDir`, `FalKey`, `FalBaseURL`, `FalImageModel`, `FalVideoModel`, `OpenAIImageKey`, `OpenAIImageBaseURL`, `OpenAIImagePath`, `OpenAIImageModel` (all `json:"...omitempty"`). r4 adds the OpenAI-compatible video fields: `OpenAIVideoKey`, `OpenAIVideoBaseURL`, `OpenAIVideoModel`, `OpenAIVideoResolution` (key/base fall back to the image fields, then to the global api_key/base_url).
  - Defaults applied when zero: `ImageProvider="auto"`, `VideoProvider="auto"`, `AudioProvider="off"`, `Order=["openai","fal","pil"]`, `MaxConcurrent` resolved per provider in the registry (1 for `pil`, 4 for cloud), `TimeoutSeconds` resolved per kind in the tool layer (120 image / 300 video), `OutputDir="outputs/media"`, `OpenAIImagePath="/images/generations"`, `OpenAIImageModel="gpt-image-1-mini"`, `FalImageModel="fal-ai/flux/schnell"`, `FalVideoModel="fal-ai/wan/v2.7/text-to-video"` (pinned r3, see MG-007), `OpenAIVideoModel="google/veo-3.1-lite"` (provider-side default when unset; override e.g. `bytedance/seedance-1-5-pro`).
  - Env overrides applied after file load (precedence: env > file): `HAKASE_MEDIA_IMAGE_PROVIDER`, `HAKASE_MEDIA_VIDEO_PROVIDER`, `HAKASE_MEDIA_OUTPUT_DIR`, `HAKASE_FAL_KEY`, `HAKASE_MEDIA_VIDEO_MODEL`. Nothing else. (`FAL_KEY`, `OPENAI_API_KEY`, `REPLICATE_API_TOKEN` are deliberately NOT read - house convention is `HAKASE_*` only.)
  - Validation: `ImageProvider` in `{"auto","pil","openai","fal","off"}`, `VideoProvider` in `{"auto","openai","fal","off"}`, `AudioProvider` in `{"off","openai","elevenlabs"}` (values other than `off` are accepted for forward-compat but resolve to a "not wired" error) - else error. `MaxConcurrent > 0` or default. `TimeoutSeconds` 0 uses per-kind default.
  - `config.json.example` documents the block with comments, including an OpenRouter example (`base_url` + `openai_image_path: "/images"`).
  - `go test ./internal/config/...` covers defaults, env precedence, invalid values.
  - **Zero-config regression test:** loading with no `media` block yields `ImageProvider=="auto"`, and `media.NewRegistry` on those defaults succeeds with `Resolve("image") == pil`.
- Affected Components: `internal/config/config.go`, `internal/config/config_test.go` (extend), `config.json.example`.
- Contracts:
  ```go
  type MediaConfig struct {
      ImageProvider     string `json:"image_provider,omitempty"`
      VideoProvider     string `json:"video_provider,omitempty"`
      AudioProvider     string `json:"audio_provider,omitempty"`
      Order             []string `json:"order,omitempty"`
      MaxConcurrent     int    `json:"max_concurrent,omitempty"`
      TimeoutSeconds    int    `json:"timeout_seconds,omitempty"`
      OutputDir         string `json:"output_dir,omitempty"`
      FalKey            string `json:"fal_key,omitempty"`
      FalBaseURL        string `json:"fal_base_url,omitempty"`
      FalImageModel     string `json:"fal_image_model,omitempty"`
      FalVideoModel     string `json:"fal_video_model,omitempty"`
      OpenAIImageKey    string `json:"openai_image_key,omitempty"`
      OpenAIImageBaseURL string `json:"openai_image_base_url,omitempty"`
      OpenAIImagePath   string `json:"openai_image_path,omitempty"`
      OpenAIImageModel  string `json:"openai_image_model,omitempty"`
  }
  func (c *MediaConfig) ApplyDefaults()
  func (c *MediaConfig) Validate() error
  ```
- Guardrails: never log raw keys; redact in any `String()`/debug rendering. Missing block must not break existing configs. Key fallback chains follow the vision pattern: `openai_image_key` empty -> `cfg.APIKey`; `openai_image_base_url` empty -> `cfg.BaseURL` -> `https://api.openai.com/v1`.
- Dependencies: none.

### Spec MG-002: Provider interface and registry

- Objective: Create `internal/media/provider.go` (interface + types) and `internal/media/registry.go` (priority `auto` resolution + semaphore + key-presence health).
- Acceptance Criteria:
  - `Provider` interface as below with `Name()`, `Capabilities()`, `GenerateImage/Video/Audio`.
  - `Capabilities` struct and `ImageRequest` / `VideoRequest` / `AudioRequest` / `MediaResult` types with validation helpers (prompt required 1-4000 chars, width/height clamped 256-2048, seed optional).
  - `Registry` constructed via `NewRegistry(cfg MediaConfig, log LogFunc, store *Store)` which builds its **provider factory map internally**. No package-level `Register()` - global mutable registration state breaks parallel tests.
  - `Resolve(kind string) (Provider, error)` for `auto`: walks `Order`, returns first provider whose `Capabilities()` covers the kind AND health check passes. Explicit name via `Get(name)` validates capability (e.g. `pil` for video returns `unsupported`).
  - Health is cheap and synchronous: `pil` always healthy; `openai`/`fal` healthy iff their key resolves (non-empty after fallback chain). No network probes in v1.
  - Semaphore: `Acquire(ctx)` / `Release()` per provider keyed by resolved `MaxConcurrent`. `auto` acquires on the chosen provider only. Respects `ctx` deadline.
  - `go test ./internal/media/...` covers: auto order, explicit, missing provider, unhealthy skip, semaphore blocking, parallel-safe construction.
- Affected Components: `internal/media/provider.go` (new), `internal/media/registry.go` (new), `internal/media/registry_test.go`.
- Contracts:
  ```go
  type Provider interface {
      Name() string
      Capabilities() Capabilities
      GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error)
      GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error)
      GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error)
  }
  type Factory func(cfg MediaConfig, log LogFunc, store *Store) (Provider, error)
  func NewRegistry(cfg MediaConfig, log LogFunc, store *Store) (*Registry, error)
  func (r *Registry) Resolve(kind string) (Provider, error)
  func (r *Registry) Get(name string) (Provider, bool)
  ```
- Guardrails: interface stays small (no `Init()` - factories do setup). `Registry` must not import provider impl files' internals beyond the factory functions in the same package.
- Dependencies: MG-001, MG-003 (Store).

### Spec MG-003: Sandbox-confined media store

- Objective: Create `internal/media/store.go` that allocates and writes media files only under sandbox-resolved `outputs/media`.
- Acceptance Criteria:
  - `NewStore(outputDir string) (*Store, error)` resolves `outputDir` through `sandbox.CurrentSandbox` when non-nil (same pattern as `internal/vision`), else `filepath.Abs`. Creates dir with `0755`; rejects paths outside allowed roots.
  - `Allocate(ext string) (string, error)` returns `<root>/<ulid><ext>` where ext in `{.png,.jpg,.webp,.mp4,.webm,.mp3,.wav}` else error. Uses `ulid.Make()` + `securejoin.SecureJoin` + `EvalSymlinks` re-check.
  - `Write(path string, r io.Reader, maxBytes int64) error` copies with `io.CopyN` + size cap (callers pass 20MB image / 100MB video), atomic tmp+rename, mode `0644`.
  - Traversal attempts (`../evil.png` as any component) are rejected; tested mirroring `sandbox` package tests.
  - `go test ./internal/media/...` covers: traversal rejection, atomic write, size cap, missing dir creation.
- Affected Components: `internal/media/store.go` (new), `internal/media/store_test.go`.
- Contracts:
  ```go
  type Store struct{ root string }
  func NewStore(outputDir string) (*Store, error)
  func (s *Store) Allocate(ext string) (string, error)
  func (s *Store) Write(path string, r io.Reader, maxBytes int64) error
  func (s *Store) Root() string
  ```
- Guardrails: never `filepath.Join` without `securejoin` for user-controlled parts. `Allocate` ignores provider-supplied filenames entirely (ULID only).
- Dependencies: MG-001 (OutputDir default).

### Spec MG-004: PIL-equivalent fallback provider (Go-native)

- Objective: Create `internal/media/pil.go` - always-available image provider rendering structured graphics in-process.
- Decision (T0.2, closed): **Go-native**, not a Python bridge. Zero pip, zero subprocess, zero cgo.
- Acceptance Criteria:
  - `Capabilities().Image == true`, others false.
  - `GenerateImage` renders requested size (default 1024x1024): background, rounded card, title from prompt (truncated ~80 chars), decorative shapes derived from prompt hash + seed. Deterministic given same prompt+seed.
  - **Text layout is an internal interface** (`textLayout`) with two implementations:
    - `simpleLayout` (required): embedded `golang.org/x/image/font/gofont/goregular` (already vendored). Latin scripts; non-Latin glyphs degrade to a visible placeholder box, documented in `support.md`.
    - `shapingLayout` (included, isolated in `textlayout.go`): system-font discovery (`/usr/share/fonts`, `/usr/local/share/fonts`, `~/.local/share/fonts`, `$XDG_DATA_HOME/fonts`) + `github.com/go-text/typesetting` (pure Go; proper Arabic joining/RTL, Indic, CJK given a covering font). Selects a font whose cmap covers the title's runes; falls back to `simpleLayout` when nothing covers. May slip to v1.1 without blocking the feature release, but the interface lands in v1.
  - Output via `Store.Allocate(".png")` + `Store.Write(..., 20MB)`.
  - Empty prompt -> error, not panic. No network access anywhere in this provider.
  - `go test`: generates image, asserts valid PNG header, exact dimensions, prompt-required error; `textlayout_test.go` asserts coverage-based font selection and fallback.
- Affected Components: `internal/media/pil.go`, `internal/media/textlayout.go` (optional), tests.
- Contracts: implements `Provider` for `Name()="pil"`.
- Guardrails: must not import ML/torch/diffusers anything. New direct dependency `github.com/go-text/typesetting` goes in `go.mod` only if `shapingLayout` ships.
- Dependencies: MG-002, MG-003.

### Spec MG-005: ComfyUI provider - **DEFERRED TO V2**

Deferred because it cannot be developed or verified without GPU hardware, a running ComfyUI install, and locally downloaded model weights - every failure mode is invisible to httptest mocks. The full design is preserved under [Deferred to v2](#deferred-to-v2) including the checkpoint-discovery mechanism that replaces the (impossible) "default checkpoint" assumption.

### Spec MG-006: OpenAI Images provider (plain net/http, OpenAI-compatible endpoints)

- Objective: Create `internal/media/openai.go` speaking the OpenAI Images REST API with plain `net/http`, working against api.openai.com **and** OpenAI-compatible routers (OpenRouter) via `base_url` + path override.
- Rationale (review findings): `openai-go/v3` is an indirect dep with zero usage in this codebase; the existing OpenAI integration (`provider.go:GetModelInfo`) is raw `net/http` over `base + path`. An SDK would assume official-API path/behavior - exactly what breaks on OpenRouter, whose image endpoint is `POST {base}/images` vs OpenAI's `POST {base}/images/generations` (request/response bodies are near-identical: `model`+`prompt` in, `data[0].b64_json` out).
- Acceptance Criteria:
  - `Capabilities().Image == true`.
  - Endpoint resolution: `(openai_image_base_url || cfg.BaseURL || "https://api.openai.com/v1") + (openai_image_path || "/images/generations")`. Auth: `Authorization: Bearer <openai_image_key || cfg.APIKey>`.
  - Request body: `{"model": <openai_image_model || req.Model>, "prompt": ..., "n": 1, "size": "<W>x<H>"}`. `response_format` is sent **only** for `dall-e-*` models (GPT image models reject it and always return b64).
  - Default model `gpt-image-1-mini`. Per-model size rules:
    | Model prefix | Allowed sizes | Clamping |
    |---|---|---|
    | `gpt-image-2*` | arbitrary `WxH`, both divisible by 16, aspect 1:3..3:1, <=3840x2160; plus `1024x1024`, `1536x1024`, `1024x1536` | snap to nearest legal |
    | `gpt-image-1*`, `gpt-image-1.5*` | `1024x1024`, `1536x1024`, `1024x1536` | nearest of trio |
    | `dall-e-3` (legacy) | `1024x1024`, `1792x1024`, `1024x1792` | nearest of three; **EOL on the official API since 2026-05-12** - rule kept only for compatible routers that still alias it |
    | anything else (custom/OpenRouter slug) | pass `size` through unchanged | endpoint clamps |
  - Decode `data[0].b64_json` -> `Store.Write(..., 20MB)`.
  - Errors (actionable, redacted): `401` -> `openai image auth failed: check api_key / openai_image_key (401)`; `404` -> hint that compatible routers use a different path: `images endpoint not found at <url>: for OpenRouter set openai_image_path to "/images" (404)`.
  - `go test` with `httptest.Server`: success (b64), 401, 404-with-hint, per-model clamping, path override, `response_format` presence only for dall-e.
- Affected Components: `internal/media/openai.go` (new), `internal/media/openai_test.go`.
- Contracts: `Name()="openai"`.
- Guardrails: reuse the `base+path` construction style from `internal/agent/provider.go`. Never log the key. No retries (Non-Goal 1).
- Dependencies: MG-002, MG-003.

### Spec MG-007: fal.ai provider

- Objective: Create `internal/media/fal.go` - fal.ai image and video via queue API + polling, downloading results through the shared SSRF-guarded fetch pattern.
- Acceptance Criteria:
  - `Capabilities().Image == true`, `Video == true`.
  - `GenerateImage`: `POST https://queue.fal.run/<fal_image_model>` (default `fal-ai/flux/schnell`) with `Authorization: Key <fal_key>`; poll `GET .../requests/<id>/status` (1s interval, bounded by timeout) until `COMPLETED`; download `response.images[0].url`.
  - `GenerateVideo`: same flow with `<fal_video_model>`. **Pinned default (r3): `fal-ai/wan/v2.7/text-to-video`** - chosen because its documented schema maps 1:1 onto `VideoRequest` and pricing is verified: $0.10/s at 720p, $0.15/s at 1080p (a default 5s clip costs ~$0.50). Request mapping for the pinned model:
    - `resolution`: `"720p"` fixed in v1 (cost control)
    - `aspect_ratio`: nearest supported of `16:9, 9:16, 1:1, 4:3, 3:4` computed from requested width:height (default `16:9`); absolute pixels are not sent - the model works in resolution tiers
    - `duration`: clamped to our 2-10 (model allows 2-15)
    - `seed`: pass-through when set
    Cheaper overridable alternates exist (Wan 2.5 ~$0.05/s, Grok Imagine Video $0.07/s @720p, LTX-2.3 Pro $0.08/s @1080p) but their mappings are untested; other slugs pass through as-is (Non-Goal 2).
  - **Download validation = shared SSRF guard, not a host allowlist**: reuse the `proxyHTTPClient` pattern from `internal/web/handlers/file.go` (enforce https, resolve and validate public IPs before dialing, re-validate every redirect hop) + `io.CopyN` cap (20MB image / 100MB video). Rationale: CSP is unaffected either way (browser only ever loads same-origin `/api/files/inline`), and hardcoding `fal.media` hosts breaks the moment fal routes outputs elsewhere.
  - Model override: `fal_base_url` (queue host), `fal_image_model` / `fal_video_model` (slugs), tool-level `model` param wins over config defaults.
  - Errors: connection refused / 401 / poll-timeout all actionable and redacted.
  - `go test` with `httptest.Server`: queue+poll+download success, 401, poll timeout, SSRF rejection (result URL pointing at a private IP is refused).
- Affected Components: `internal/media/fal.go` (new), `internal/media/fal_test.go`.
- Contracts: `Name()="fal"`.
- Guardrails: `context.WithTimeout` on every HTTP call. No retries (Non-Goal 1). Never log the key.
- Dependencies: MG-002, MG-003.

### Spec MG-008: ADK tools (generate_image / generate_video / generate_audio)

- Objective: Create `internal/media/tools.go` exposing three ADK tools via `util.NewDocTool` (note: exported symbol in `internal/util`; same mechanism as knowledge/task tools).
- Acceptance Criteria:
  - `generate_image` Input: `prompt` (required, 1-4000 chars), `negative_prompt?`, `width?` (256-2048, default 1024), `height?` (256-2048, default 1024), `steps?` (1-50, default 20), `seed?` (int64), `provider?` ("auto" default | explicit), `model?`. Output: `path`, `provider`, `model`, `seed`, `width`, `height`, `mime_type`, plus a `markdown` snippet `![generated](outputs/media/<ulid>.png)` for the agent to echo.
  - `generate_video` Input: `prompt` (required, 1-4000 chars), `duration_seconds?` (2-10, default 5), `width?`, `height?`, `provider?`, `model?`, `image?` (local path or http(s)/data URL anchoring the first frame via OpenRouter-style `frame_images`; fal is text-to-video only). Output same shape, `.mp4`. Providers: `openai` (r4 async jobs API: `POST {base}/videos` -> poll `polling_url` -> download `unsigned_urls[0]` or `{base}/videos/{id}/content`) and `fal` (`fal-ai/wan/v2.7/text-to-video`). No provider available -> `video generation requires a provider: configure an OpenAI-compatible router with video support (media.openai_video_key / openai_video_base_url, e.g. OpenRouter), or set fal_key (HAKASE_FAL_KEY) with media.video_provider fal` (exact string tested).
  - `generate_audio` Input: `text` (required), `voice?`, `provider?`. `audio_provider:"off"` -> `audio generation is off: set media.audio_provider to openai once TTS is wired (planned v2)`. Any other audio value in v1 -> `audio generation is not wired in this build: openai TTS is planned for v2`.
  - `image_provider:"off"` -> `image generation is off: set media.image_provider to auto, pil, openai, or fal`.
  - All tools: validate inputs, clamp sizes, `Registry.Resolve`, acquire semaphore, wrap handler in `context.WithTimeout` (**per-kind timeout resolved here in the tool layer: 120s image / 300s video**), append to `outputs/media/manifest.jsonl` (**mutex-guarded; exactly one formatted line per append** - concurrent cloud generations share the file), return result. Errors are actionable `tool.Error`s, never raw stack traces.
  - `go test` covers: valid image via pil, video/audio/off error strings verbatim, size clamping, prompt required, provider override, manifest append under concurrency, timeout releases semaphore.
- Affected Components: `internal/media/tools.go` (new), `internal/media/tools_test.go`.
- Contracts:
  ```go
  func CreateMediaTools(reg *Registry, log LogFunc) ([]tool.Tool, error)
  // Input structs carry json + doc tags; descriptions injected by util.NewDocTool
  ```
- Guardrails: tool descriptions steer the LLM to prefer `generate_image` for infographic/poster tasks. No keys in tool descriptions. On `reg == nil` return an error (defensive; see MG-009 - this path should never trigger in production).
- Dependencies: MG-001..MG-004, MG-006, MG-007.

### Spec MG-009: Agent wiring (bridge factory)

- Objective: Wire config -> registry -> tools into the harness following the repo's bridge-factory convention (AGENTS.md: "new agent-facing cross-package capabilities usually need a factory added there").
- Acceptance Criteria:
  - `cmd/hakase/main.go` constructs the registry **unconditionally** after config load: `mediaReg, err := media.NewRegistry(cfg.Media, log, store)`; on error log `WARN [media] disabled: ...` and continue startup with nil registry. Wire `CreateMediaToolsFn: func(log interfaces.LogFunc) ([]tool.Tool, error) { return media.CreateMediaTools(mediaReg, log) }` onto `agent.Deps`.
  - `internal/agent/deps.go` adds only `CreateMediaToolsFn func(log interfaces.LogFunc) ([]tool.Tool, error)`. `internal/agent` does **not** import `internal/media` (no import cycle by construction, and agent tests stay decoupled).
  - `internal/agent/agent.go` `setupRunner` appends `deps.CreateMediaToolsFn(log)` results to orchestrator tools (orchestrator only in v1, like knowledge tools). Nil-safe: nil factory or nil registry -> zero media tools, no panic.
  - `buildOrchestratorInstruction` adds `### MEDIA GENERATION:` after `### KNOWLEDGE BASE:` describing when to use the tools and that `pil` is the offline fallback.
  - `go build ./...` green; `go test ./internal/agent/...` covers: tool count includes 3 media tools when factory wired, instruction contains MEDIA section, nil factory safe.
  - The MG-001 zero-config regression test is extended end-to-end: empty config -> defaults -> registry -> `Resolve("image")==pil` -> `CreateMediaTools` returns 3 tools.
- Affected Components: `cmd/hakase/main.go`, `internal/agent/deps.go`, `internal/agent/agent.go`, `internal/agent/agent_instruction_test.go`.
- Contracts: no import cycle (media never imports agent).
- Guardrails: r1's "registry nil when media block absent" rule is **deleted** - it contradicted the zero-config DoD and silently disabled env-only configuration. The registry always exists; only construction failure disables media.
- Dependencies: MG-001, MG-002, MG-008.

### Spec MG-010: Settings UI and status endpoint

- Objective: Expose media status and allow configuration from the web UI.
- Acceptance Criteria:
  - `GET /api/media/status` (auth-gated) returns resolved providers and per-provider capability/configured flags, **never raw keys**:
    ```json
    {"image_provider":"auto","video_provider":"auto","audio_provider":"off",
     "resolved_image":"pil","resolved_video":"none","resolved_audio":"off",
     "capabilities":{"pil":{"image":true},
                     "openai":{"image":true,"configured":false},
                     "fal":{"image":true,"video":true,"configured":false}},
     "output_dir":"outputs/media"}
    ```
    Resolution reads registry state only - no network probes (there are none in v1 anyway).
  - `GET /api/media/manifest` (optional v1): last 20 entries of `outputs/media/manifest.jsonl`.
  - `webui/src/views/SettingsView.vue` Media section: selects for `image_provider` / `video_provider` / `audio_provider`, password inputs for `fal_key` / `openai_image_key`, text inputs for `openai_image_base_url` / `openai_image_path` / `openai_image_model` / `fal_base_url` / `fal_image_model` / `fal_video_model`, text input for `order`, numbers for `max_concurrent` / `timeout_seconds`. Reuses the existing `updateConfig` flow and the `has_*_key` boolean pattern (mirrors `has_vision_api_key`). Shows resolved badges + "pil always available" hint.
  - `webui/src/lib/api.ts` adds `getMediaStatus()` / `getMediaManifest()`.
  - `internal/web/handlers/media_test.go` covers status auth, resolved fields, no key leakage.
- Affected Components: `internal/web/handlers/media.go` (new), `internal/web/server.go` (register route in auth group), `webui/src/views/SettingsView.vue`, `webui/src/lib/api.ts`.
- Guardrails: `configured: true/false` booleans only.
- Dependencies: MG-001, MG-002.

### Spec MG-011: Docs, skills, and support matrix

- Objective: Document the feature, update skills, publish the support matrix.
- Acceptance Criteria:
  - `docs/media-generation/fixtures.md` carries verified payloads for pil / openai (official + OpenRouter-path variant) / fal image / fal video / error strings / manifest entry / status JSON, including **one live capture per cloud provider with an available account** as the merge gate (OpenRouter at merge time; official-OpenAI and fal captures are deferred until accounts exist and do not block the merge).
  - `.agents/skills/baoyu-infographic/SKILL.md` updated: prefer `generate_image` with prompt orchestration, fall back to HTML/SVG when the tool is unavailable. The `comfyui` skill is **not** modified in v1.
  - `README.md`: TODO section updated - `image_gen`/`video_gen` shipped (cloud + local fallback; ComfyUI deferred), Features gains a media bullet, `media` config block + env vars documented.
  - `docs/media-generation/support.md` published: provider matrix, env vars, troubleshooting (401, 404 path hint, size caps, cost, org-verification note for GPT Image models, OpenRouter setup example).
- Guardrails: README reflects actual defaults (`auto` -> cloud-if-keyed else `pil`).
- Dependencies: all.

## Deferred to v2

### ComfyUI provider (former MG-005) - design preserved

Deferred rationale: requires GPU hardware, a running ComfyUI instance, and locally downloaded weights to develop against; httptest mocks cannot catch its real failure modes (r1's fixture hardcoded `sd_xl_base_1.0.safetensors`, which fails on any install lacking that exact file).

Design notes for the future implementer:

- **Checkpoints cannot be defaulted.** ComfyUI validates `ckpt_name` against the filenames in the server's `models/checkpoints/` directory at submit time; an unknown name returns HTTP 400 `value_not_in_list` with the valid list embedded in `node_errors`.
- **Discovery is mandatory:** `GET {url}/object_info/CheckpointLoaderSimple` -> `input.required.ckpt_name[0]` is the array of installed filenames. Parse defensively (tuple arity varies by ComfyUI version). Pick explicit `comfyui_checkpoint` config if set (validated against the list), else first entry. Fetch per generation (loopback HTTP, cheap; lists change as users add models).
- Empty model list -> actionable error ("add a model to ComfyUI/models/checkpoints").
- Workflow JSON: build minimally (KSampler / CheckpointLoaderSimple / EmptyLatentImage / CLIPTextEncode x2 / VAEDecode / SaveImage), inject discovered `ckpt_name`.
- Health probe `GET /system_stats` (1s) with cached state; status endpoint reads the cache, never probes inline.
- SSRF: loopback/private only unless explicitly allowed.
- Video via AnimateDiff additionally requires custom-node detection - treat as a separate v2 investigation, not a free byproduct of image support.

### Also deferred

- Replicate provider (`replicate_token` + `HAKASE_REPLICATE_TOKEN` when added).
- Audio/TTS wiring (OpenAI `/v1/audio/speech`, ElevenLabs).
- `max_cost_cents_per_run` guard.
- Async job API (`--async` + jobIds + SSE delivery).

## Execution Sequence

1. **MG-001** (blocks 002, 003, 008, 009, 010) - config first.
2. **MG-003** (blocks 002, 004, 006, 007) - store before providers.
3. **MG-002** (blocks 004, 006, 007, 008) - registry before providers/tools.
4. **MG-004, MG-006, MG-007** in parallel (depend only on 002+003); MG-010 can start after 001+002.
5. **MG-008** (depends on 001,002,004,006,007) - tools after providers.
6. **MG-009** (depends on 008) - wiring.
7. **MG-011** (depends on all) - docs last.

## Open Questions and Risks

1. ~~Go native PIL vs Python bridge~~ RESOLVED (r2): Go-native; embedded gofont required, `go-text/typesetting` shaping layer included but isolated.
2. ~~ComfyUI workflow fragility~~ RESOLVED by deferral; discovery design preserved above.
3. ~~fal video model slug~~ RESOLVED r3: `fal-ai/wan/v2.7/text-to-video` (schema-verified mapping, $0.10/s @720p, ~$0.50 typical 5s clip).
4. **GPT Image org verification** CONFIRMED CURRENT (r3 research): GPT Image models require one-time organization verification (platform.openai.com, Settings > Organization > General); allow up to 30 minutes propagation (the older 15-minute figure is outdated); generate a fresh API key afterwards; test in the Images playground before application code. `gpt-image-1`/`-mini` available on tiers 1-5 subject to verification; `gpt-image-1.5` requires Tier 1+. **Not applicable when routing through OpenRouter** - no OpenAI organization is involved. Also confirmed: dall-e-2/dall-e-3 were retired from the official API on 2026-05-12; the `/v1/images/generations` endpoint remains for GPT Image models.
5. **Cost visibility**: `auto` tries cloud before `pil` whenever a key resolves (including an OpenRouter `api_key` via fallback). Mitigated by surfacing `provider` + `model` in every tool result and in `/api/media/status`; hard cost guard deferred to v2.

## Quality Control Checklist

- [ ] Each atomic spec is independently testable (MG-008, MG-009, MG-010 especially).
- [x] Dependency map reflects real files (verified by read of `internal/agent`, `internal/config`, `internal/web`, `internal/sandbox`, `webui`).
- [x] Guardrails are specific (CSP untouched by design, sandbox paths named, redact rules named, env-var convention named).
- [x] Sequence respects dependencies.
- [x] An engineer unfamiliar with the project could execute any spec standalone (see tasks.md sizing).
