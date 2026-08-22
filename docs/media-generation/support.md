# Support Matrix: Media Generation

Feature: `media-generation` operational guide (r2 scope: `pil` + `openai` + `fal`; ComfyUI deferred to v2). Provider matrix, configuration, env vars, troubleshooting, playbook.

## Provider Support Matrix

| Capability | `pil` | `openai` (incl. OpenRouter) | `fal` | v2: `comfyui` / `replicate` |
|---|---|---|---|---|
| Image | yes (structured graphics) | yes | yes | planned |
| Video | no | yes (async jobs API: OpenRouter `/videos`, OpenAI `/videos/{id}/content`) | yes (text-to-video only) | planned |
| Image-to-video | no | yes (first frame via `frame_images`) | no (actionable error) | planned |
| Audio | no | planned (TTS, v2) | planned | planned |
| Edit / img2img | no | video: first-frame anchoring; richer refs later | later | planned |
| Runs where | inside hakase process | provider cloud GPUs | fal cloud GPUs | user GPU (comfyui) |
| Requires network | no | yes | yes | comfyui: loopback only |
| Cost per call | free | ~$0.01-0.08/img; video from $0.03/s (veo-3.1-lite @720p, audio off) | ~$0.003+/img; video ~$0.10/s | free (local) |
| `auto` priority | 3 (fallback) | 1 | 2 | - |

### Default resolution

- `image_provider: "auto"` walks `order` (`["openai","fal","pil"]`) and picks the first healthy provider. Health = key presence (pil always healthy). **Consequence:** if your chat key is an OpenAI/OpenRouter key, the fallback chain makes image generation billable by default - every tool result names the provider and model used, and `GET /api/media/status` shows `resolved_image`.
- `video_provider: "auto"` prefers `openai` (OpenRouter/OpenAI-compatible video jobs) when a key resolves, then `fal`, and returns an actionable error when neither is configured (no pil for video).
- `audio_provider: "off"` returns its stub message; other values report "not wired in this build" (v2).

### When to use which

- **Offline / no keys / diagrams-posters-slides**: `pil`. Not photorealistic. Latin text always renders; Arabic/CJK render when a covering system font is installed (shaping layer), otherwise degrade to placeholder boxes.
- **Photoreal images**: `openai` against api.openai.com or any compatible router. OpenRouter gives one key/bill across GPT Image, Gemini Image, FLUX, Seedream, and more.
- **Cheapest images**: `fal` flux-schnell.
- **Video (cheapest)**: `openai` against OpenRouter with the default `google/veo-3.1-lite` slug - $0.03/s @720p with `generate_audio:false` (a 4s minimum clip is ~$0.12). Cheaper-per-second alternates: `x-ai/grok-imagine-video` ($0.05/s @480p, flexible 1-15s durations), `bytedance/seedance-1-5-pro` (per-token pricing, 480p). Pricing verified against `GET /api/v1/videos/models` on 2026-08-21; re-check before cost-sensitive runs.

## Configuration

### `config.json` `media` block

```json
{
  "media": {
    "image_provider": "auto",
    "video_provider": "auto",
    "audio_provider": "off",
    "order": ["openai", "fal", "pil"],
    "max_concurrent": 4,
    "timeout_seconds": 120,
    "output_dir": "outputs/media",

    "openai_image_key": "",
    "openai_image_base_url": "",
    "openai_image_path": "/images/generations",
    "openai_image_model": "gpt-image-1-mini",

    "openai_video_key": "",
    "openai_video_base_url": "",
    "openai_video_model": "google/veo-3.1-lite",
    "openai_video_resolution": "720p",

    "fal_key": "",
    "fal_base_url": "",
    "fal_image_model": "fal-ai/flux/schnell",
    "fal_video_model": "<pinned at implementation; see fixtures.md>"
  }
}
```

OpenRouter example (image generation under your existing chat key):

```json
{
  "base_url": "https://openrouter.ai/api/v1",
  "api_key": "sk-or-...",
  "media": {
    "openai_image_path": "/images"
  }
}
```

Fields:

| Field | Default | Description |
|---|---|---|
| `image_provider` | `auto` | `auto` / `pil` / `openai` / `fal` / `off` |
| `video_provider` | `auto` | `auto` / `openai` / `fal` / `off` |
| `audio_provider` | `off` | `off` now; `openai`/`elevenlabs` reserved for v2 |
| `order` | `["openai","fal","pil"]` | `auto` priority walk |
| `max_concurrent` | 1 for pil / 4 for cloud | Per-provider semaphore |
| `timeout_seconds` | 120 image / 300 video | Resolved per kind in the tool layer |
| `output_dir` | `outputs/media` | Sandbox-resolved; must be under workspace roots |
| `openai_image_key` | falls back to `api_key` | Bearer for the images endpoint |
| `openai_image_base_url` | falls back to `base_url`, then `https://api.openai.com/v1` | Any OpenAI-compatible host |
| `openai_image_path` | `/images/generations` | Set `/images` for OpenRouter |
| `openai_image_model` | `gpt-image-1-mini` | See model table below |
| `openai_video_key` | `openai_image_key`, then `api_key` | Bearer for the async videos endpoint |
| `openai_video_base_url` | `openai_image_base_url`, then `base_url`, then api.openai.com | Host serving `POST {base}/videos` (OpenRouter: keep the inherited `https://openrouter.ai/api/v1`) |
| `openai_video_model` | `google/veo-3.1-lite` | Cheapest confirmed OpenRouter slug; override e.g. `bytedance/seedance-1-5-pro`, `alibaba/wan-2.7`; also via `HAKASE_MEDIA_VIDEO_MODEL` |
| `openai_video_resolution` | `720p` | Priced tier for most models; set `"auto"` to omit and use the server default |
| `fal_key` | - | Also settable via `HAKASE_FAL_KEY` |
| `fal_base_url` | `https://queue.fal.run` | Queue host override |
| `fal_image_model` | `fal-ai/flux/schnell` | fal slug |
| `fal_video_model` | `fal-ai/wan/v2.7/text-to-video` | $0.10/s @720p (~$0.50 typical 5s clip); cheaper untested alternates via override |

### Image model sizing rules (openai provider)

| Model | Sizes accepted | Notes |
|---|---|---|
| `gpt-image-2*` | arbitrary `WxH` (both divisible by 16, aspect 1:3..3:1, <=3840x2160) plus `1024x1024`/`1536x1024`/`1024x1536` | Best quality; may require OpenAI org verification |
| `gpt-image-1*`, `gpt-image-1.5*` | trio above | Always returns b64; `response_format` not sent |
| `dall-e-3` (legacy) | `1024x1024`, `1792x1024`, `1024x1792` | **Retired from the official API 2026-05-12**; rule kept for routers that alias it; `response_format:"b64_json"` sent |
| custom slugs (e.g. OpenRouter `bytedance-seed/seedream-4.5`) | pass-through | Endpoint clamps; check router's model catalog for pricing |

### Environment variables (precedence: env > file)

| Env | Field |
|---|---|
| `HAKASE_MEDIA_IMAGE_PROVIDER` | `image_provider` |
| `HAKASE_MEDIA_VIDEO_PROVIDER` | `video_provider` |
| `HAKASE_MEDIA_VIDEO_MODEL` | `openai_video_model` |
| `HAKASE_MEDIA_OUTPUT_DIR` | `output_dir` |
| `HAKASE_FAL_KEY` | `fal_key` |

Note: hakase deliberately does not read `OPENAI_API_KEY`, `FAL_KEY`, or other provider-native env names. The OpenAI-side key comes from `openai_image_key` or your existing `api_key` (settable via `HAKASE_API_KEY`).

### User-home config

`~/.hakase/config.json` is a **whole-file fallback**: it is used only when no project `config.json` exists. There is no field-level merge between the two files.

## Troubleshooting

### `generate_image` uses `pil` even though I expected cloud

- Check `GET /api/media/status`: `capabilities.openai.configured` / `capabilities.fal.configured` show whether a key resolved.
- Remember the fallback chain: `openai_image_key` empty means your chat `api_key` is used - if that key is valid for the configured endpoint, `openai` should be healthy.
- Explicitly set `image_provider` to skip `auto` ordering entirely.

### `401 openai image auth failed`

- Your `api_key` fallback is for a different service than the endpoint (e.g. Gemini key against api.openai.com). Set `openai_image_key` explicitly.
- On OpenRouter, ensure the key has credits; generation failures are not billed but auth/quota errors still 401/402.

### `images endpoint not found ... (404)`

- You are pointing at a compatible router whose path differs. For OpenRouter set `"openai_image_path": "/images"` in the `media` block.

### OpenAI returns 403 on GPT Image models

- GPT Image models require one-time organization verification (confirmed current as of 2026): platform.openai.com -> Settings > Organization > General -> verify; allow **up to 30 minutes** for propagation (older 15-minute guidance is outdated); then generate a **fresh API key** and test in the Images playground before application code.
- `gpt-image-1` / `gpt-image-1-mini`: available on usage tiers 1-5 subject to verification. `gpt-image-1.5`: Tier 1+ required, no free tier.
- No OpenAI account? Route through OpenRouter instead - no OpenAI organization or verification is involved (`openai_image_path: "/images"`).

### `dall-e-3` requests fail

- Expected: dall-e-2/dall-e-3 were retired from the official OpenAI API on 2026-05-12. Use a GPT Image model (default) or an OpenRouter image slug. The dall-e clamp rule exists only for routers that still alias the name.

### `video generation requires a provider`

- Expected when neither an OpenAI-compatible video key nor `fal_key` resolves. With OpenRouter, the inherited chat `api_key` + `base_url` are usually enough - check `/api/media/status`. For fal, set `fal_key` (config or `HAKASE_FAL_KEY`). ComfyUI video is deferred to v2.

### `openai video generation failed: duration N is not supported`

- Video models only accept specific clip lengths (veo 4/6/8s, wan 5/10s, grok 1-15s). Retry with a supported `duration_seconds`; the error quotes the model's allowed values.

### `provider fal does not support image-to-video`

- fal's wired pipeline is text-to-video only. For image-to-video (first-frame anchoring of a generated image), omit the fal hint and let `auto` resolve `openai` (OpenRouter), or pass `provider: "openai"`.

### Generated media not showing in chat

- Path must be `outputs/media/<ulid>.<ext>` served via `/api/files/inline`; `mediaLinks` rewrites automatically. Check `manifest.jsonl` - missing entry means generation failed.
- Non-Latin title text shows boxes: install a font covering that script (e.g. `noto-sans-arabic`, Noto CJK); the shaping layer picks it up on the next generation.

### Slow / timeout

- Defaults: 120s image, 300s video. Raise `timeout_seconds` for slow models. `manifest.jsonl` `duration_ms` near the cap is the signal.
- No automatic retries exist by design; just ask the agent to retry after a failure.

### Key in logs

- Should never happen. Config debug output redacts keys; `/api/media/status` returns `configured:true/false` only. If seen, file a bug.

## Operational Playbook

### Adding a new provider

1. Create `internal/media/<name>.go` implementing `Provider` (copy `openai.go` as the HTTP template).
2. Add its factory to the map inside `NewRegistry`; extend `MediaConfig.Validate` allowlists; add config fields + env override following `HAKASE_*` convention.
3. Downloads must go through the shared SSRF-guard fetch pattern; writes through `Store`; `context.WithTimeout` everywhere; no retries.
4. Add `httptest.Server` mocks; update this matrix, SettingsView selector, and fixtures.

Do not add per-provider tools - extend the single `generate_*` negotiation.

### Cost control

- v1 relies on visibility: every tool result carries `provider` and `model`; `/api/media/status` shows what `auto` will pick. `auto` tries cloud before `pil` whenever a key resolves - if zero spend matters, set `image_provider: "pil"` explicitly.
- v2 plans `max_cost_cents_per_run` enforced against provider-reported pricing.

### Observability

- Audit: `logs/exec-audit.jsonl` gains `media_provider`, `media_model`, `media_duration_ms` per call.
- Manifest: `outputs/media/manifest.jsonl` append-only; `GET /api/media/manifest` tails the last 20 entries.
- Status: `GET /api/media/status` - resolved providers + capability/configured flags, no probes, no keys.

### Rollout

- Opt-in by omission: existing configs without a `media` block get defaults (`auto` -> cloud-if-keyed else `pil`) with no breakage; the registry is always constructed.
- Web UI Settings -> Media guides key setup; status badges show resolution live.
- `.agents/skills/baoyu-infographic` prefers `generate_image`, falls back to HTML/SVG when the tool is absent. The `comfyui` skill remains doctrine-only until v2.
