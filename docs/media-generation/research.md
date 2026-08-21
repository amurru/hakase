# Research: Pluggable Media Generation for Hakase

Status: Research complete, revised r3 (2026-08-21). Feeds `spec.md`, `plan.md`, `tasks.md`.
Feature: `media-generation`
r2 changes: ComfyUI deferred to v2; Go-native PIL confirmed; key handling aligned to house patterns (`HAKASE_*` only); OpenRouter image-path compatibility researched and specced; current OpenAI image model lineup verified.
r3 changes: fal video slug pinned from live catalog research (`fal-ai/wan/v2.7/text-to-video`); dall-e-2/dall-e-3 confirmed retired from the official API on 2026-05-12; GPT Image org-verification guidance confirmed current (30-min propagation, fresh key afterwards, not applicable via OpenRouter).

## Goal

Close the README TODO (deferred `image_gen` / `video_gen` tools) by adding a **pluggable, provider-agnostic media generation layer** that:

1. Exposes `generate_image`, `generate_video` (and stub `generate_audio`) as ADK tools to the orchestrator.
2. Works offline on day one via a **Go-native** drawing fallback (`pil`), works best when the user brings a provider (OpenAI / OpenAI-compatible routers like OpenRouter, or fal.ai).
3. Is sandboxed, observable, and bounded - never blocks the agent loop beyond its timeout, never leaks keys, never writes outside `outputs/media/`.

Unlocks:
- `baoyu-infographic` (native images instead of the HTML/SVG workaround)
- `comfyui` - **not unlocked in v1** (deferred; skill stays doctrine-only)
- `songwriting-and-ai-music` - audio remains external in v1 (stub tool returns actionable message)

## Current State (baseline)

- `internal/agent` has `python_interpreter` with isolated `.venv`, auto pip install, sandbox-pinned execution. Proven with PIL/matplotlib workloads.
- `internal/config` pattern: `provider` + `model_name` + `base_url` + `api_key`, with per-feature overrides (`vision_provider` / `vision_base_url` / `vision_api_key`) falling back to the primary key. Env overrides are exclusively `HAKASE_*` (`HAKASE_API_KEY`, `HAKASE_VISION_API_KEY`, ...). **The program never reads `OPENAI_API_KEY`, `FAL_KEY`, or any provider-native env name** - r1's proposal to do so was rejected in review.
- `internal/sandbox` enforces roots via `securejoin` + `EvalSymlinks`; package-level `sandbox.CurrentSandbox` is the established resolution hook (used by vision).
- `internal/web/handlers/file.go` serves `outputs/` via `/api/files/inline` (symlink-safe, regular-files-only) and fetches arbitrary external images through `proxyHTTPClient` with full SSRF guarding (scheme enforcement, public-IP dial validation, redirect re-check per hop). That client is the reuse template for provider downloads.
- `webui/src/lib/markdown/plugins/mediaLinks.ts` rewrites workspace-relative media links to `/api/files/inline`; CSP is `img-src 'self' data: https:` + `media-src 'self' data:`. Generated media renders with zero frontend viewer work.
- `golang.org/x/image` v0.44.0 and `oklog/ulid/v2` v2.1.2 already in `go.mod`. `openai-go/v3` is present but **indirect with zero imports** - there is no SDK client-construction pattern to reuse; the existing OpenAI integration is raw `net/http` over `base + path` (`provider.go:GetModelInfo`).

## Provider Landscape

### Image (v1)

| Provider | API | Auth | Strengths | Weaknesses | Verdict |
|---|---|---|---|---|---|
| **pil fallback** | in-process Go drawing | none | Zero deps, offline, deterministic, instant, fully unit-testable | Not photorealistic; Latin-only without shaping layer | **Default fallback - always works** |
| **openai** (any OpenAI-compatible images endpoint) | REST `POST {base}{path}` | bearer key from `api_key` chain | Reuses existing key/base_url infra; OpenRouter gives 30+ models under one key; failed OpenRouter requests unbilled | Cost per image; path differs across routers (`/images/generations` vs `/images`) -> `openai_image_path` override | **Primary cloud** |
| **fal.ai** | queue REST + poll | `fal_key` / `HAKASE_FAL_KEY` | Fast, cheap (Flux Schnell ~$0.003/img), image+video under one key | Extra key; queue polling; output hosts vary -> SSRF-guard download, no allowlist | **Secondary cloud + only v1 video** |

### Video (v1)

| Provider | Latency | Cost | Verdict |
|---|---|---|---|
| **fal.ai** | 30s-minutes | per-second pricing; **pinned default `fal-ai/wan/v2.7/text-to-video`**: $0.10/s @720p, $0.15/s @1080p (~$0.50 for the default 5s clip) | Only v1 video path |
| ComfyUI AnimateDiff | 30-120s | local GPU | **Deferred to v2** (hardware-gated; custom-node detection is its own project) |

fal video catalog snapshot (r3 research, for override decisions): Wan 2.5 ~$0.05/s, Grok Imagine Video $0.07/s @720p, Kling 2.5 Turbo Pro $0.07/s, PixVerse V6 $0.06/s, LTX-2.3 Pro $0.08/s @1080p, Happy Horse 1.0 $0.14/s @720p, Seedance 2.0 $0.3034/s @720p, Veo 3.1 $0.40/s, Sora 2 Pro $0.30-0.70/s. Wan 2.7 was pinned because its published schema maps 1:1 onto our `VideoRequest` (prompt, duration 2-15s covering our 2-10, resolution enum, aspect_ratio enum, seed) - the cheaper alternates lack verified schemas and stay untested pass-through options.

### Audio

Stub in v1. OpenAI TTS (`/v1/audio/speech`) is the natural v2 wiring; ElevenLabs after.

### Deferred to v2

- **ComfyUI**: requires GPU hardware, a running install, and locally downloaded weights to develop against; httptest mocks cannot catch its real failure modes. Full design preserved in `spec.md` "Deferred to v2" - critically, checkpoints **cannot be defaulted** (ComfyUI validates `ckpt_name` against installed filenames at submit time), so the future implementation must discover via `GET /object_info/CheckpointLoaderSimple`.
- Replicate, Stability: polling patterns add surface without immediate need.

## Technology Decisions

### 1. Provider Interface (Go) - The Core Abstraction

One interface, one tool per modality:

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error)
    GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error)
    GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error)
}
```

Capabilities enable negotiation (`auto` picks first covering provider); `MediaResult.Path` is always under `outputs/media/` via the sandbox-confined store. Rejected: per-provider tools (explodes tool count, forces the LLM to pick providers).

### 2. Registry (factory map, no globals)

`NewRegistry(cfg, log, store)` builds its factory map internally. r1's package-level `Register(name, factory)` was rejected: global mutable registration state plus parallel tests is a flaky-suite generator, and it makes mock injection awkward. Health is key-presence only in v1 (pil always healthy; cloud healthy iff key resolves) - no network probes, so the status endpoint never blocks.

### 3. Config Schema (house conventions)

See MG-001 contract in `spec.md`. Key decisions:

- **Key fallback chains mirror vision**: `openai_image_key` empty -> `cfg.APIKey`; `openai_image_base_url` empty -> `cfg.BaseURL`. This means an OpenRouter user's existing chat key/base_url automatically route image generation - intended behavior for this repo's primary tester.
- **Env vars are `HAKASE_*` only**: `HAKASE_MEDIA_IMAGE_PROVIDER`, `HAKASE_MEDIA_VIDEO_PROVIDER`, `HAKASE_MEDIA_OUTPUT_DIR`, `HAKASE_FAL_KEY`. Provider-native names (`OPENAI_API_KEY`, `FAL_KEY`, `REPLICATE_API_TOKEN`) deliberately not read.
- **Path override for compatible routers**: `openai_image_path` (default `/images/generations`; OpenRouter users set `/images`). Verified against OpenRouter docs (2026-06 dedicated Image API launch): request/response bodies are near-identical to OpenAI's (`model`+`prompt` in, `data[0].b64_json` out), only the path differs.
- Default order `["openai","fal","pil"]`.

### 4. Execution Model - Sync with Bounded Timeout

Synchronous with `context.WithTimeout` resolved in the tool layer (120s image / 300s video). Cancellation propagates to HTTP calls. No automatic retries ever (paid, non-idempotent calls; retry policy belongs to the agent). Async job API deferred to v2.

### 5. Storage - Sandbox-Confined MediaStore

ULID filenames only (provider-supplied filenames ignored - traversal-proof by construction). Atomic capped writes. Served via existing `/api/files/inline`. Manifest appends mutex-guarded, one line per write (concurrent cloud generations share the file).

### 6. Security - Key Handling and Downloads

- Keys flow only through Go HTTP headers; `HAKASE_*` scrubbing for subprocesses already covers everything.
- Downloads use the shared SSRF-guard fetch pattern instead of hostname allowlists: fal's output hosts can drift, and the codebase already safely fetches arbitrary public URLs for `/api/files/proxy`. CSP is unaffected either way - the browser only ever loads same-origin inline URLs.
- Redaction everywhere keys could appear (config debug rendering, status endpoint uses `configured` booleans).

### 7. Frontend - No New View in v1

Existing `mediaLinks` + `/api/files/inline` render everything. SettingsView gains a Media section mirroring the Vision section's structure (including the `has_*_key` boolean pattern).

## Open Policy Questions (resolved)

1. **Default `image_provider`?** RESOLVED r2: `auto`, order `["openai","fal","pil"]`. pil guarantee means zero-config works; cloud tried first whenever a key resolves (including via the `api_key` fallback). Cost implication stated honestly rather than hidden: provider+model surfaced in every tool result and in `/api/media/status`; hard cost guard deferred to v2.
2. **Default `video_provider`?** RESOLVED: `auto` errors actionably when nothing is configured (no pil for video).
3. **pil quality?** RESOLVED: structured infographics/posters via drawing primitives; documented as diagrams-not-photos. Text layout: embedded gofont baseline (Latin), optional shaping layer for Arabic/CJK behind an internal interface.
4. ~~ComfyUI discovery~~ MOOT in v1 (deferred); design preserved in spec.md.
5. **Cost guard?** DEFERRED to v2 (`max_cost_cents_per_run`). v1 relies on visibility (tool result fields, status endpoint) and cheap defaults.
6. **Key conventions?** RESOLVED r2: house-pattern only. No provider-native env names.
7. **Default image models?** RESOLVED r2/r3 from current API research: `openai_image_model` defaults to `gpt-image-1-mini` (cheapest sane tier; GPT Image models always return b64 and reject `response_format`). **dall-e-2/dall-e-3 were retired from the official API on 2026-05-12** - the dall-e clamp rule and `response_format` gating survive only for compatible routers that alias legacy models. fal image defaults to `fal-ai/flux/schnell`; fal video pinned r3 to `fal-ai/wan/v2.7/text-to-video`.
8. **GPT Image org verification?** CONFIRMED CURRENT r3: one-time organization verification required for GPT Image models (Settings > Organization > General); up to 30 minutes propagation; generate a fresh API key afterwards; test in the Images playground first. gpt-image-1/-mini: tiers 1-5 subject to verification; gpt-image-1.5: Tier 1+ (no free tier). Irrelevant when routing through OpenRouter - no OpenAI org involved, which is the v1 testing path.

## Rejected Alternatives

- **Separate tool per provider**: explodes tool count, duplicates auth/retry.
- **Shell out to `comfy-cli`**: fragile, unsandboxable.
- **`openai-go/v3` SDK for images**: indirect dep with zero usage; SDK assumptions break on OpenRouter's different path; raw `net/http` matches the codebase and keeps the router override trivial.
- **Reading `OPENAI_API_KEY` / native provider env vars**: foreign convention; the program's rule is `HAKASE_*` only.
- **Hostname allowlist for fal downloads**: breaks on storage-host drift; SSRF guard achieves the security goal without the fragility.
- **Package-level provider registration**: global mutable state vs parallel tests.
- **Nil registry when config block absent**: contradicted the zero-config guarantee and silently disabled env-only configuration. Registry is unconditional.
- **Server-side queuing DB, new frontend gallery, bundled model weights**: unchanged from r1 - all rejected.
