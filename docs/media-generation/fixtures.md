# Fixtures: Media Generation QA

Feature: `media-generation` (r3 scope: `pil` + `openai` + `fal`)
Spec: MG-004, MG-006..MG-011. This file records before/after for every provider payload and the QA matrix sign-off.
Output captured from `go test ./internal/media/...`, mock servers, and live runs. Live captures are the merge gate for MG-011.

## Provider Payloads

### PIL fallback (generate_image, zero config)

Input:
```json
{"prompt": "a poster for baoyu infographic about Tokyo transit", "width": 1024, "height": 1024}
```

Expected tool output:
```json
{"path": "outputs/media/01H...png", "provider": "pil", "model": "pil-v1", "width": 1024, "height": 1024, "mime_type": "image/png", "markdown": "![generated](outputs/media/01H...png)"}
```

File checks: valid PNG header `89 50 4E 47`, exact dimensions, deterministic bytes given same prompt+seed. Rendered in chat as `<img src="/api/files/inline?path=outputs%2Fmedia%2F...png">` via `mediaLinks`.

Text layout: Latin via embedded gofont; Arabic/CJK titles render when a covering system font exists (shaping layer), else placeholder boxes (documented degradation).

### OpenAI Images - official endpoint (generate_image)

Mock request (`POST https://api.openai.com/v1/images/generations`):
```
Authorization: Bearer sk-...
{"model": "gpt-image-1-mini", "prompt": "a poster...", "n": 1, "size": "1024x1024"}
```
Note: no `response_format` field for GPT Image models (they always return b64 and reject the parameter).

Mock response:
```json
{"created": 1234567890, "data": [{"b64_json": "iVBORw0KGgo..."}]}
```

Decoded bytes -> `Store.Write(..., 20MB)`.

Per-model clamping fixtures:

| Request | Model | Sent `size` |
|---|---|---|
| 512x512 | gpt-image-1-mini | `1024x1024` |
| 2000x1000 | dall-e-3 (legacy alias) | `1792x1024` (+ body includes `"response_format":"b64_json"`) - official API retired the model 2026-05-12; fixture covers router aliases only |
| 1536x864 | gpt-image-2 | `1536x864` (legal arbitrary: divisible by 16, aspect within bounds) |
| 2048x2048 | custom router slug | `2048x2048` pass-through |

Error 401:
```
openai image auth failed: check api_key / openai_image_key (401)
```

Error 404 (wrong path for a compatible router):
```
images endpoint not found at https://openrouter.ai/api/v1/images/generations: for OpenRouter set openai_image_path to "/images" (404)
```

### OpenAI Images - OpenRouter variant (generate_image)

Config: `base_url: "https://openrouter.ai/api/v1"`, `openai_image_path: "/images"` (key falls back to chat `api_key`).

Mock request (`POST https://openrouter.ai/api/v1/images`):
```json
{"model": "bytedance-seed/seedream-4.5", "prompt": "a red panda astronaut", "n": 1, "size": "1024x1024"}
```

Response shape identical (`data[0].b64_json`). Failed requests are not billed by OpenRouter.

### fal.ai image (generate_image)

Mock flow (default `fal_image_model: "fal-ai/flux/schnell"`):
- `POST https://queue.fal.run/fal-ai/flux/schnell` with `Authorization: Key <fal_key>` -> `{"request_id": "req_123"}`
- `GET https://queue.fal.run/fal-ai/flux/schnell/requests/req_123/status` poll -> `{"status": "COMPLETED", "response": {"images": [{"url": "https://v3.fal.media/files/.../image.png"}]}}`
- Download via shared SSRF-guard client (https + public IP + redirect re-check) -> `Store.Write(..., 20MB)`

SSRF fixture: result URL rewritten to `https://127.0.0.1/x.png` or any private IP -> refused before dialing.

### fal.ai video (generate_video)

Pinned default (r3): **`fal-ai/wan/v2.7/text-to-video`** - $0.10/s @720p, $0.15/s @1080p (~$0.50 for the default 5s clip). Chosen for a verified schema mapping 1:1 onto `VideoRequest`.

Request mapping (the one tested mapping):
```json
{"prompt": "...", "resolution": "720p", "aspect_ratio": "16:9", "duration": 5}
```
- `resolution` fixed `"720p"` in v1; `aspect_ratio` = nearest of `16:9, 9:16, 1:1, 4:3, 3:4` from requested width:height (absolute pixels not sent); `duration` clamped to our 2-10 (model allows 2-15); `seed` passed when set.

Flow mirrors image: queue POST -> status poll (1s interval, bounded by video timeout 300s) -> download `.mp4` (100MB cap) -> renders as `<video controls src="/api/files/inline?path=...">`. Other slugs pass through untested.

### Error states (verbatim strings, asserted in tools_test.go)

No video provider:
```json
{"error": "video generation requires a provider: set media.video_provider to fal and set fal_key (HAKASE_FAL_KEY), or configure an OpenAI-compatible image router"}
```

Audio off:
```json
{"error": "audio generation is off: set media.audio_provider to openai once TTS is wired (planned v2)"}
```

Audio wired-later value:
```json
{"error": "audio generation is not wired in this build: openai TTS is planned for v2"}
```

Image off:
```json
{"error": "image generation is off: set media.image_provider to auto, pil, openai, or fal"}
```

Timeout: `context.DeadlineExceeded` surfaced as tool error; semaphore released (asserted).

### Manifest entry

Append to `outputs/media/manifest.jsonl` per call (mutex-guarded, one line per write):
```json
{"ts":"2026-08-21T02:00:00Z","tool":"generate_image","prompt":"a poster...","provider":"pil","path":"outputs/media/01H...png","width":1024,"height":1024,"duration_ms":42}
```

Concurrency fixture: N parallel generations -> file parses as N valid JSON lines.

### Status endpoint

`GET /api/media/status` (auth required):
```json
{"image_provider":"auto","video_provider":"auto","audio_provider":"off",
 "resolved_image":"pil","resolved_video":"none","resolved_audio":"off",
 "capabilities":{"pil":{"image":true},
                 "openai":{"image":true,"configured":false},
                 "fal":{"image":true,"video":true,"configured":false}},
 "output_dir":"outputs/media"}
```

With keys present, `resolved_image` flips to the highest-priority healthy provider and `configured:true` appears. No raw keys under any configuration.

## Live Captures (merge gate)

Order reflects account availability: OpenRouter is runnable now; official-OpenAI and fal captures are deferred until accounts exist.

| Capture | Endpoint | Expected spend | Status |
|---|---|---|---|
| pil PNG | local | free | TODO (any time) |
| OpenRouter image (e.g. `bytedance-seed/seedream-4.5`) | `POST {base}/images` | ~$0.01-0.05 | TODO - **runnable now** |
| Official OpenAI image (`gpt-image-1-mini`) | `/v1/images/generations` | ~$0.02-0.08 | DEFERRED - needs OpenAI account (+ org verification for GPT Image models) |
| fal image (flux/schnell) | queue.fal.run | ~$0.003 | DEFERRED - needs fal account |
| fal video (wan-v2.7, 5s @720p) | queue.fal.run | ~$0.50 | DEFERRED - needs fal account |

Record per capture: request body sent, response body (redacted/truncated), resulting manifest line, screenshot of inline render in ChatView.

## QA Matrix Sign-off

| Payload | Expected | Status |
|---|---|---|
| `generate_image` zero config | pil PNG, `provider:"pil"`, renders inline | TODO |
| `generate_image` explicit `provider:"pil"` + size | clamped, pil path | TODO |
| `generate_image` with `HAKASE_FAL_KEY` + auto | fal chosen, `resolved_image:"fal"` | TODO |
| `generate_image` via OpenRouter path config | POST hits `{base}/images`, b64 decoded | TODO |
| `generate_image` dall-e-3 odd size | clamped, `response_format` present | TODO |
| `generate_image` gpt-image model | no `response_format` in body | TODO |
| `provider:"openai"` bad key | verbatim 401 message | TODO |
| Images 404 wrong path | verbatim hint names `openai_image_path` | TODO |
| `generate_video` no provider | verbatim requires-provider error | TODO |
| `generate_video` with fal | `.mp4` + `<video controls>` | TODO |
| `generate_audio` off / wired-later | verbatim stub messages | TODO |
| `GET /api/media/status` no auth | 401 | TODO |
| `GET /api/media/status` auth | resolved fields, zero raw keys | TODO |
| Traversal attempt on Store | rejected | TODO |
| fal private-IP result URL | SSRF-refused | TODO |
| Timeout | DeadlineExceeded, semaphore released | TODO |
| Concurrent generations | manifest intact | TODO |

---

## V2 Annex (preserved design - not executed in v1)

### ComfyUI (former MG-005)

Deferred r2: untestable without GPU hardware + running ComfyUI + locally downloaded weights. Key design facts preserved so the future implementer does not rediscover them:

**Checkpoints cannot be defaulted.** ComfyUI validates `ckpt_name` against installed filenames at submit time; unknown name -> HTTP 400:
```json
{"error": {"type": "invalid_prompt", "details": "Value not in list: ckpt_name: 'sd_xl_base_1.0.safetensors' not in [...]"},
 "node_errors": {"4": {"errors": [{"type": "value_not_in_list", ...}]}}}
```
(r1's hardcoded workflow JSON failed on every install lacking that exact file.)

**Discovery:** `GET {url}/object_info/CheckpointLoaderSimple` -> `input.required.ckpt_name[0]` = array of installed filenames. Parse defensively (tuple arity varies across ComfyUI versions). Pick `comfyui_checkpoint` config if set (validated against list), else first entry. Fetch per generation (loopback, cheap). Empty list -> actionable error.

Minimal workflow skeleton (inject discovered name at `"4".inputs.ckpt_name`):
```json
{"prompt": {"3": {"inputs": {"seed": 42, "steps": 20, "cfg": 7, "sampler_name": "euler", "scheduler": "normal", "denoise": 1, "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}, "class_type": "KSampler"},
 "4": {"inputs": {"ckpt_name": "<discovered>"}, "class_type": "CheckpointLoaderSimple"},
 "5": {"inputs": {"width": 1024, "height": 1024, "batch_size": 1}, "class_type": "EmptyLatentImage"},
 "6": {"inputs": {"text": "<prompt>", "clip": ["4", 1]}, "class_type": "CLIPTextEncode"},
 "7": {"inputs": {"text": "", "clip": ["4", 1]}, "class_type": "CLIPTextEncode"},
 "8": {"inputs": {"samples": ["3", 0], "vae": ["4", 2]}, "class_type": "VAEDecode"},
 "9": {"inputs": {"filename_prefix": "hakase", "images": ["8", 0]}, "class_type": "SaveImage"}}}
```

Flow: `POST /prompt` with `client_id` -> poll `GET /history/{prompt_id}` (500ms) -> `GET /view?filename=...` -> Store. Health probe `GET /system_stats` (1s) cached; status endpoint reads cache only. SSRF: loopback/private only unless explicitly allowed. Video (AnimateDiff) additionally needs custom-node detection - separate investigation.
