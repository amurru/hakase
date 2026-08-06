---
name: comfyui
description: "Generate images, video, and audio via diffusion workflows using ComfyUI. Use when the user asks to generate images with Stable Diffusion/SDXL/Flux/SD3, run ComfyUI workflows, chain generative steps (txt2img -> upscale -> face restore), use ControlNet/inpainting/img2img, manage ComfyUI queue, check models, install custom nodes, or generate video/audio/3D via AnimateDiff/Hunyuan/Wan. Requires user-installed ComfyUI infrastructure (local GPU or Comfy Cloud API key)."
license: MIT
metadata:
  author: 'hakase (ported via Hermes Agent; original Hermes skill by kshitijk4poor, alt-glitch, purzbeats, MIT)'
  version: 1.0.0
  source: https://github.com/NousResearch/hermes-agent/tree/main/skills/creative/comfyui
---

# ComfyUI

Generate images, video, audio, and 3D content through ComfyUI using the
official `comfy-cli` for setup/lifecycle and direct REST/WebSocket API
for workflow execution.

## Infrastructure Gate (ALWAYS CHECK FIRST)

**This skill requires user-installed infrastructure.** Before executing any
comfy-related command, FIRST check whether the tools are available:

```bash
# Check if comfy-cli is installed
system_exec command="which comfy" 2>/dev/null && echo "comfy-cli: installed" || echo "comfy-cli: NOT installed"
system_exec command="which uvx" 2>/dev/null && echo "uvx: installed" || echo "uvx: NOT installed"

# Check if ComfyUI server is reachable
system_exec command="curl -s http://127.0.0.1:8188/system_stats" 2>/dev/null && echo "server: running" || echo "server: NOT running"

# Check hardware capability
system_exec command="python3 .agents/skills/comfyui/scripts/hardware_check.py"
```

**If neither comfy-cli, uvx, nor a running server is found**, do NOT fail
silently. Instead, explain the setup requirements to the user:

- **Local**: NVIDIA GPU with >=6 GB VRAM (>=8 GB for SDXL, >=12 GB for Flux/video),
  OR AMD GPU with ROCm (Linux), OR Apple Silicon Mac (M1+) with >=16 GB unified
  memory. Intel Macs and machines with no GPU will NOT work locally.
- **Cloud**: Comfy Cloud (https://comfy.org/cloud) -- hosted on RTX 6000 Pro,
  zero install, paid subscription required for API access. Requires
  `export COMFY_CLOUD_API_KEY="comfyui-..."`.
- **Setup**: After hardware is confirmed, run `system_exec command="python3
  .agents/skills/comfyui/scripts/hardware_check.py"` then follow the
  appropriate installation path below.

Ask the user which path they prefer before proceeding.

## What's in this skill

**Reference docs (`references/`):**

- `official-cli.md` -- every `comfy ...` command, with flags
- `rest-api.md` -- REST + WebSocket endpoints (local + cloud), payload schemas
- `workflow-format.md` -- API-format JSON, common node types, param mapping
- `template-integrity.md` -- converting `comfyui-workflow-templates` from
  editor format to API format: Reroute bypass, dotted dynamic-input keys
  (`values.a`, `resize_type.width`), Cloud quirks (302 redirect, 1 concurrent
  free-tier job, 1080p VRAM ceiling), Discord-compatible ffmpeg stitch.
  Authored by [@purzbeats](https://github.com/purzbeats). Load this whenever
  you're starting from an official template.

**Scripts (`scripts/`):** All located at `.agents/skills/comfyui/scripts/`.
Run them via `system_exec command="python3 .agents/skills/comfyui/scripts/<script>.py ..."`.

| Script | Purpose |
|--------|---------|
| `_common.py` | Shared HTTP, cloud routing, node catalogs (don't run directly) |
| `hardware_check.py` | Probe GPU/VRAM/disk -> recommend local vs Comfy Cloud |
| `comfyui_setup.sh` | Hardware check + comfy-cli + ComfyUI install + launch + verify |
| `extract_schema.py` | Read a workflow -> list controllable params + model deps |
| `check_deps.py` | Check workflow against running server -> list missing nodes/models |
| `auto_fix_deps.py` | Run check_deps then `comfy node install` / `comfy model download` |
| `run_workflow.py` | Inject params, submit, monitor, download outputs (HTTP or WS) |
| `run_batch.py` | Submit a workflow N times with sweeps, parallel up to your tier |
| `ws_monitor.py` | Real-time WebSocket viewer for executing jobs (live progress) |
| `health_check.py` | Verification checklist runner -- comfy-cli + server + models + smoke test |
| `fetch_logs.py` | Pull traceback / status messages for a given prompt_id |

**Example workflows (`workflows/`):** SD 1.5, SDXL, Flux Dev, SDXL img2img,
SDXL inpaint, ESRGAN upscale, AnimateDiff video, Wan T2V. See
`workflows/README.md`.

## When to Use

- User asks to generate images with Stable Diffusion, SDXL, Flux, SD3, etc.
- User wants to run a specific ComfyUI workflow file
- User wants to chain generative steps (txt2img -> upscale -> face restore)
- User needs ControlNet, inpainting, img2img, or other advanced pipelines
- User asks to manage ComfyUI queue, check models, or install custom nodes
- User wants video/audio/3D generation via AnimateDiff, Hunyuan, Wan, AudioCraft, etc.

## Architecture: Two Layers

```
+-------------------------------------------------------+
| Layer 1: comfy-cli (official lifecycle tool)           |
|   Setup, server lifecycle, custom nodes, models        |
|   -> comfy install / launch / stop / node / model      |
+---------------------------+---------------------------+
                            |
+---------------------------v---------------------------+
| Layer 2: REST/WebSocket API + skill scripts            |
|   Workflow execution, param injection, monitoring      |
|   POST /api/prompt, GET /api/view, WS /ws              |
|   -> run_workflow.py, run_batch.py, ws_monitor.py      |
+-------------------------------------------------------+
```

**Why two layers?** The official CLI is excellent for installation and server
management but has minimal workflow execution support. The REST/WS API fills
that gap -- the scripts handle param injection, execution monitoring, and
output download that the CLI doesn't do.

## Quick Start

### Detect environment

```bash
# What's available? (use system_exec)
system_exec command="command -v comfy >/dev/null 2>&1 && echo 'comfy-cli: installed' || echo 'comfy-cli: not found'"
system_exec command="curl -s http://127.0.0.1:8188/system_stats 2>/dev/null && echo 'server: running' || echo 'server: not reachable'"

# Can this machine run ComfyUI locally? (GPU/VRAM/disk check)
system_exec command="python3 .agents/skills/comfyui/scripts/hardware_check.py"
```

If nothing is installed, see **Infrastructure Gate** above and **Setup & Onboarding** below -- but always run the
hardware check first.

### One-line health check

```bash
system_exec command="python3 .agents/skills/comfyui/scripts/health_check.py"
# -> JSON: comfy_cli on PATH? server reachable? at least one checkpoint? smoke-test passes?
```

## Core Workflow

### Step 1: Get a workflow JSON in API format

Workflows must be in API format (each node has `class_type`). They come from:

- ComfyUI web UI -> **Workflow -> Export (API)** (newer UI) or
  the legacy "Save (API Format)" button (older UI)
- This skill's `workflows/` directory (ready-to-run examples)
- Community downloads (civitai, Reddit, Discord) -- usually editor format,
  must be loaded into ComfyUI then re-exported

Editor format (top-level `nodes` and `links` arrays) is **not directly
executable**. The scripts detect this and tell you to re-export.

### Step 2: See what's controllable

```bash
system_exec command="python3 .agents/skills/comfyui/scripts/extract_schema.py workflows/sdxl_txt2img.json --summary-only"
# -> {"parameter_count": 12, "has_negative_prompt": true, "has_seed": true, ...}

system_exec command="python3 .agents/skills/comfyui/scripts/extract_schema.py workflows/sdxl_txt2img.json"
# -> full schema with parameters, model deps, embedding refs
```

### Step 3: Run with parameters

```bash
# Local (defaults to http://127.0.0.1:8188)
system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/sdxl_txt2img.json \
  --args '{\"prompt\": \"a beautiful sunset over mountains\", \"seed\": -1, \"steps\": 30}' \
  --output-dir ./outputs"

# Cloud (export API key once; uses correct /api routing automatically)
export COMFY_CLOUD_API_KEY="comfyui-..."
system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/sdxl_txt2img.json \
  --args '{\"prompt\": \"...\"}' \
  --host https://cloud.comfy.org \
  --output-dir ./outputs"

# Real-time progress via WebSocket (requires `pip install websocket-client`)
system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/flux_dev_txt2img.json \
  --args '{\"prompt\": \"...\"}' \
  --ws"

# img2img / inpaint: pass --input-image to upload + reference automatically
system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/sdxl_img2img.json \
  --input-image image=./photo.png \
  --args '{\"prompt\": \"make it watercolor\", \"denoise\": 0.6}'"

# Batch / sweep: 8 random seeds, parallel up to cloud tier limit
system_exec command="python3 .agents/skills/comfyui/scripts/run_batch.py \
  --workflow .agents/skills/comfyui/workflows/sdxl_txt2img.json \
  --args '{\"prompt\": \"abstract\"}' \
  --count 8 --randomize-seed --parallel 3 \
  --output-dir ./outputs/batch"
```

`-1` for `seed` (or omitting it with `--randomize-seed`) generates a fresh
random seed per run.

### Step 4: Present results

The scripts emit JSON to stdout describing every output file:

```json
{
  "status": "success",
  "prompt_id": "abc-123",
  "outputs": [
    {"file": "./outputs/sdxl_00001_.png", "node_id": "9",
     "type": "image", "filename": "sdxl_00001_.png"}
  ]
}
```

## Decision Tree

| User says | Tool | Command |
|-----------|------|---------|
| **Lifecycle (use comfy-cli)** | | |
| "install ComfyUI" | comfy-cli | `system_exec command="bash .agents/skills/comfyui/scripts/comfyui_setup.sh"` |
| "start ComfyUI" | comfy-cli | `system_exec command="comfy launch --background"` |
| "stop ComfyUI" | comfy-cli | `system_exec command="comfy stop"` |
| "install X node" | comfy-cli | `system_exec command="comfy node install <name>"` |
| "download X model" | comfy-cli | `system_exec command="comfy model download --url <url> --relative-path models/checkpoints"` |
| "list installed models" | comfy-cli | `system_exec command="comfy model list"` |
| "list installed nodes" | comfy-cli | `system_exec command="comfy node show installed"` |
| **Execution (use scripts)** | | |
| "is everything ready?" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/health_check.py"` (optionally `--workflow X --smoke-test`) |
| "what can I change in this workflow?" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/extract_schema.py W.json"` |
| "check if W's deps are met" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/check_deps.py W.json"` |
| "fix missing deps" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/auto_fix_deps.py W.json"` |
| "generate an image" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py --workflow W --args '...'"` |
| "use this image" (img2img) | script | `system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py --input-image image=./x.png ..."` |
| "8 variations with random seeds" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/run_batch.py --count 8 --randomize-seed ..."` |
| "show me live progress" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/ws_monitor.py --prompt-id <id>"` |
| "fetch the error from job X" | script | `system_exec command="python3 .agents/skills/comfyui/scripts/fetch_logs.py <prompt_id>"` |
| **Direct REST** | | |
| "what's in the queue?" | REST | `system_exec command="curl http://HOST:8188/queue"` (local) or `--host https://cloud.comfy.org` |
| "cancel that" | REST | `system_exec command="curl -X POST http://HOST:8188/interrupt"` |
| "free GPU memory" | REST | `system_exec command="curl -X POST http://HOST:8188/free"` |

## Setup & Onboarding

When a user asks to set up ComfyUI, **the FIRST thing to do is ask whether
they want Comfy Cloud (hosted, zero install, API key) or Local (install
ComfyUI on their machine)**. Don't start running install commands or hardware
checks until they've answered.

**Official docs:** https://docs.comfy.org/installation
**CLI docs:** https://docs.comfy.org/comfy-cli/getting-started
**Cloud docs:** https://docs.comfy.org/get_started/cloud
**Cloud API:** https://docs.comfy.org/development/cloud/overview

### Step 0: Ask Local vs Cloud (ALWAYS FIRST)

Suggested script:

> "Do you want to run ComfyUI locally on your machine, or use Comfy Cloud?
>
> - **Comfy Cloud** -- hosted on RTX 6000 Pro GPUs, all common models pre-installed,
>   zero setup. Requires an API key (paid subscription required to actually run
>   workflows; free tier is read-only). Best if you don't have a capable GPU.
> - **Local** -- free, but your machine MUST meet the hardware requirements:
>   - NVIDIA GPU with **>=6 GB VRAM** (>=8 GB for SDXL, >=12 GB for Flux/video), OR
>   - AMD GPU with ROCm support (Linux), OR
>   - Apple Silicon Mac (M1+) with **>=16 GB unified memory** (>=32 GB recommended).
>   - Intel Macs and machines with no GPU will NOT work -- use Cloud instead.
>
> Which would you like?"

Routing:

- **Cloud** -> skip to **Path A**.
- **Local** -> run hardware check first, then pick a path from Paths B-E based on the verdict.
- **Unsure** -> run the hardware check and let the verdict decide.

### Step 1: Verify Hardware (ONLY if user chose local)

```bash
system_exec command="python3 .agents/skills/comfyui/scripts/hardware_check.py --json"
# Optional: also probe `torch` for actual CUDA/MPS:
system_exec command="python3 .agents/skills/comfyui/scripts/hardware_check.py --json --check-pytorch"
```

| Verdict    | Meaning                                                       | Action |
|------------|---------------------------------------------------------------|--------|
| `ok`       | >=8 GB VRAM (discrete) OR >=32 GB unified (Apple Silicon)       | Local install -- use `comfy_cli_flag` from report |
| `marginal` | SD1.5 works; SDXL tight; Flux/video unlikely                  | Local OK for light workflows, else **Path A (Cloud)** |
| `cloud`    | No usable GPU, <6 GB VRAM, <16 GB Apple unified, Intel Mac, Rosetta Python | **Switch to Cloud** unless user explicitly forces local |

The script also surfaces `wsl: true` (WSL2 with NVIDIA passthrough) and
`rosetta: true` (x86_64 Python on Apple Silicon -- must reinstall as ARM64).

If verdict is `cloud` but the user wants local, do not proceed silently.
Show the `notes` array verbatim and ask whether they want to (a) switch to
Cloud or (b) force a local install (will OOM or be unusably slow on modern models).

### Choosing an Installation Path

Use the hardware check first. The table below is the fallback for when the
user has already told you their hardware:

| Situation | Recommended Path |
|-----------|------------------|
| `verdict: cloud` from hardware check | **Path A: Comfy Cloud** |
| No GPU / want to try without commitment | **Path A: Comfy Cloud** |
| Windows + NVIDIA + non-technical | **Path B: ComfyUI Desktop** |
| Windows + NVIDIA + technical | **Path C: Portable** or **Path D: comfy-cli** |
| Linux + any GPU | **Path D: comfy-cli** (easiest) |
| macOS + Apple Silicon | **Path B: Desktop** or **Path D: comfy-cli** |
| Headless / server / CI / agents | **Path D: comfy-cli** |

For the fully automated path (hardware check -> install -> launch -> verify):

```bash
system_exec command="bash .agents/skills/comfyui/scripts/comfyui_setup.sh"
# Or with overrides:
system_exec command="bash .agents/skills/comfyui/scripts/comfyui_setup.sh --m-series --port=8190 --workspace=/data/comfy"
```

It runs `hardware_check.py` internally, refuses to install locally when the
verdict is `cloud` (unless `--force-cloud-override`), picks the right
`comfy-cli` flag, and prefers `pipx`/`uvx` over global `pip` to avoid polluting
system Python.

---

### Path A: Comfy Cloud (No Local Install)

For users without a capable GPU or who want zero setup. Hosted on RTX 6000 Pro.

**Docs:** https://docs.comfy.org/get_started/cloud

1. Sign up at https://comfy.org/cloud
2. Generate an API key at https://platform.comfy.org/login
3. Set the key:
   ```bash
   system_exec command="export COMFY_CLOUD_API_KEY='comfyui-xxxxxxxxxxxx'"
   ```
4. Run workflows:
   ```bash
   system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
     --workflow .agents/skills/comfyui/workflows/flux_dev_txt2img.json \
     --args '{\"prompt\": \"...\"}' \
     --host https://cloud.comfy.org \
     --output-dir ./outputs"
   ```

**Pricing:** https://www.comfy.org/cloud/pricing
**Concurrent jobs:** Free/Standard 1, Creator 3, Pro 5. Free tier
**cannot run workflows via API** -- only browse models. Paid subscription
required for `/api/prompt`, `/api/upload/*`, `/api/view`, etc.

---

### Path B: ComfyUI Desktop (Windows / macOS)

One-click installer for non-technical users. Currently Beta.

**Docs:** https://docs.comfy.org/installation/desktop
- **Windows (NVIDIA):** https://download.comfy.org/windows/nsis/x64
- **macOS (Apple Silicon):** https://comfy.org

Linux is **not supported** for Desktop -- use Path D.

---

### Path C: ComfyUI Portable (Windows Only)

**Docs:** https://docs.comfy.org/installation/comfyui_portable_windows

Download from https://github.com/comfyanonymous/ComfyUI/releases, extract,
run `run_nvidia_gpu.bat`. Update via `update/update_comfyui_stable.bat`.

---

### Path D: comfy-cli (All Platforms -- Recommended for Agents)

The official CLI is the best path for headless/automated setups.

**Docs:** https://docs.comfy.org/comfy-cli/getting-started

#### Install comfy-cli

```bash
# Recommended:
system_exec command="pipx install comfy-cli"
# Or use uvx without installing:
system_exec command="uvx --from comfy-cli comfy --help"
# Or (if pipx/uvx unavailable):
system_exec command="pip install --user comfy-cli"
```

Disable analytics non-interactively:
```bash
system_exec command="comfy --skip-prompt tracking disable"
```

#### Install ComfyUI

```bash
system_exec command="comfy --skip-prompt install --nvidia"              # NVIDIA (CUDA)
system_exec command="comfy --skip-prompt install --amd"                 # AMD (ROCm, Linux)
system_exec command="comfy --skip-prompt install --m-series"            # Apple Silicon (MPS)
system_exec command="comfy --skip-prompt install --cpu"                 # CPU only (slow)
system_exec command="comfy --skip-prompt install --nvidia --fast-deps"  # uv-based dep resolution
```

Default location: `~/comfy/ComfyUI` (Linux), `~/Documents/comfy/ComfyUI`
(macOS/Win). Override with `comfy --workspace /custom/path install`.

#### Launch / verify

```bash
system_exec command="comfy launch --background"                       # background daemon on :8188
system_exec command="comfy launch -- --listen 0.0.0.0 --port 8190"   # LAN-accessible custom port
system_exec command="curl -s http://127.0.0.1:8188/system_stats"      # health check
```

---

### Path E: Manual Install (Advanced / Unsupported Hardware)

For Ascend NPU, Cambricon MLU, Intel Arc, or other unsupported hardware.

**Docs:** https://docs.comfy.org/installation/manual_install

```bash
system_exec command="git clone https://github.com/comfyanonymous/ComfyUI.git"
system_exec command="pip install torch torchvision torchaudio --extra-index-url https://download.pytorch.org/whl/cu130" workdir="./ComfyUI"
system_exec command="pip install -r requirements.txt" workdir="./ComfyUI"
system_exec command="python main.py" workdir="./ComfyUI"
```

---

### Post-Install: Download Models

```bash
# SDXL (general purpose, ~6.5 GB)
system_exec command="comfy model download \
  --url 'https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/resolve/main/sd_xl_base_1.0.safetensors' \
  --relative-path models/checkpoints"

# SD 1.5 (lighter, ~4 GB, good for 6 GB cards)
system_exec command="comfy model download \
  --url 'https://huggingface.co/stable-diffusion-v1-5/stable-diffusion-v1-5/resolve/main/v1-5-pruned-emaonly.safetensors' \
  --relative-path models/checkpoints"

# Flux Dev fp8 (smaller variant, ~12 GB)
system_exec command="comfy model download \
  --url 'https://huggingface.co/Comfy-Org/flux1-dev/resolve/main/flux1-dev-fp8.safetensors' \
  --relative-path models/checkpoints"

# CivitAI (set token first):
system_exec command="comfy model download \
  --url 'https://civitai.com/api/download/models/128713' \
  --relative-path models/checkpoints \
  --set-civitai-api-token 'YOUR_TOKEN'"
```

List installed: `system_exec command="comfy model list"`.

### Post-Install: Install Custom Nodes

```bash
system_exec command="comfy node install comfyui-impact-pack"             # popular utility pack
system_exec command="comfy node install comfyui-animatediff-evolved"     # video generation
system_exec command="comfy node install comfyui-controlnet-aux"          # ControlNet preprocessors
system_exec command="comfy node install comfyui-essentials"              # common helpers
system_exec command="comfy node update all"
system_exec command="comfy node install-deps --workflow=workflow.json"   # install everything a workflow needs
```

### Post-Install: Verify

```bash
system_exec command="python3 .agents/skills/comfyui/scripts/health_check.py"
# -> comfy_cli on PATH? server reachable? checkpoints? smoke test?

system_exec command="python3 .agents/skills/comfyui/scripts/check_deps.py my_workflow.json"
# -> are this workflow's nodes/models/embeddings installed?

system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/sd15_txt2img.json \
  --args '{\"prompt\": \"test\", \"steps\": 4}' \
  --output-dir ./test-outputs"
```

## Image Upload (img2img / Inpainting)

The simplest way is to use `--input-image` with `run_workflow.py`:

```bash
system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/sdxl_img2img.json \
  --input-image image=./photo.png \
  --args '{\"prompt\": \"make it cyberpunk\", \"denoise\": 0.6}'"
```

The flag uploads `photo.png`, then injects its server-side filename into
whatever schema parameter is named `image`. For inpainting, pass both:

```bash
system_exec command="python3 .agents/skills/comfyui/scripts/run_workflow.py \
  --workflow .agents/skills/comfyui/workflows/sdxl_inpaint.json \
  --input-image image=./photo.png \
  --input-image mask_image=./mask.png \
  --args '{\"prompt\": \"fill with flowers\"}'"
```

Manual upload via REST:
```bash
system_exec command="curl -X POST 'http://127.0.0.1:8188/upload/image' \
  -F 'image=@photo.png' -F 'type=input' -F 'overwrite=true'"
# Returns: {"name": "photo.png", "subfolder": "", "type": "input"}

# Cloud equivalent:
system_exec command="curl -X POST 'https://cloud.comfy.org/api/upload/image' \
  -H 'X-API-Key: \$COMFY_CLOUD_API_KEY' \
  -F 'image=@photo.png' -F 'type=input' -F 'overwrite=true'"
```

## Cloud Specifics

- **Base URL:** `https://cloud.comfy.org`
- **Auth:** `X-API-Key` header (or `?token=KEY` for WebSocket)
- **API key:** set `$COMFY_CLOUD_API_KEY` once and the scripts pick it up automatically
- **Output download:** `/api/view` returns a 302 to a signed URL; the scripts
  follow it and strip `X-API-Key` before fetching from the storage backend
  (don't leak the API key to S3/CloudFront).
- **Endpoint differences from local ComfyUI:**
  - `/api/object_info`, `/api/queue`, `/api/userdata` -- **403 on free tier**;
    paid only.
  - `/history` is renamed to `/history_v2` on cloud (the scripts route
    automatically).
  - `/models/<folder>` is renamed to `/experiment/models/<folder>` on cloud
    (the scripts route automatically).
  - `clientId` in WebSocket is currently ignored -- all connections for a
    user receive the same broadcast. Filter by `prompt_id` client-side.
  - `subfolder` is accepted on uploads but ignored -- cloud has a flat namespace.
- **Concurrent jobs:** Free/Standard: 1, Creator: 3, Pro: 5. Extras queue
  automatically. Use `run_batch.py --parallel N` to saturate your tier.

## Queue & System Management

```bash
# Local
system_exec command="curl -s http://127.0.0.1:8188/queue | python3 -m json.tool"
system_exec command="curl -X POST http://127.0.0.1:8188/queue -d '{\"clear\": true}'"    # cancel pending
system_exec command="curl -X POST http://127.0.0.1:8188/interrupt"                       # cancel running
system_exec command="curl -X POST http://127.0.0.1:8188/free \
  -H 'Content-Type: application/json' \
  -d '{\"unload_models\": true, \"free_memory\": true}'"

# Cloud -- same paths under /api/, plus:
system_exec command="python3 .agents/skills/comfyui/scripts/fetch_logs.py --tail-queue --host https://cloud.comfy.org"
```

## Pitfalls

1. **API format required** -- every script and the `/api/prompt` endpoint expect
   API-format workflow JSON. The scripts detect editor format (top-level
   `nodes` and `links` arrays) and tell you to re-export via
   "Workflow -> Export (API)" (newer UI) or "Save (API Format)" (older UI).

2. **Server must be running** -- all execution requires a live server.
   `comfy launch --background` starts one. Verify with
   `curl http://127.0.0.1:8188/system_stats`.

3. **Model names are exact** -- case-sensitive, includes file extension.
   `check_deps.py` does fuzzy matching (with/without extension and folder
   prefix), but the workflow itself must use the canonical name. Use
   `comfy model list` to discover what's installed.

4. **Missing custom nodes** -- "class_type not found" means a required node
   isn't installed. `check_deps.py` reports which package to install;
   `auto_fix_deps.py` runs the install for you.

5. **Working directory** -- `comfy-cli` auto-detects the ComfyUI workspace.
   If commands fail with "no workspace found", use
   `comfy --workspace /path/to/ComfyUI <command>` or
   `comfy set-default /path/to/ComfyUI`.

6. **Cloud free-tier API limits** -- `/api/prompt`, `/api/view`, `/api/upload/*`,
   `/api/object_info` all return 403 on free accounts. `health_check.py` and
   `check_deps.py` handle this gracefully and surface a clear message.

7. **Timeout for video/audio workflows** -- auto-detected when an output node
   is `VHS_VideoCombine`, `SaveVideo`, etc.; the default jumps from 300 s to
   900 s. Override explicitly with `--timeout 1800`.

8. **Path traversal in output filenames** -- server-supplied filenames are
   passed through `safe_path_join` to refuse anything escaping `--output-dir`.
   Keep this protection on -- workflows with custom save nodes can produce
   arbitrary paths.

9. **Workflow JSON is arbitrary code** -- custom nodes run Python, so
   submitting an unknown workflow has the same trust profile as `eval`.
   Inspect workflows from untrusted sources before running.

10. **Auto-randomized seed** -- pass `seed: -1` in `--args` (or use
    `--randomize-seed` and omit the seed) to get a fresh seed per run.
    The actual seed is logged to stderr.

11. **`tracking` prompt** -- first run of `comfy` may prompt for analytics.
    Use `comfy --skip-prompt tracking disable` to skip non-interactively.
    `comfyui_setup.sh` does this for you.

12. **Infrastructure required** -- this skill does NOT bundle ComfyUI. It
    requires user-installed infrastructure (comfy-cli + ComfyUI + models, or
    Comfy Cloud API key). Always run the infrastructure gate check first.

## Verification Checklist

Use `system_exec command="python3 .agents/skills/comfyui/scripts/health_check.py"` to run the whole list at once. Manual:

- [ ] `hardware_check.py` verdict is `ok` OR the user explicitly chose Comfy Cloud
- [ ] `comfy --version` works (or `uvx --from comfy-cli comfy --help`)
- [ ] `curl http://HOST:PORT/system_stats` returns JSON
- [ ] `comfy model list` shows at least one checkpoint (local) OR
      `/api/experiment/models/checkpoints` returns models (cloud)
- [ ] Workflow JSON is in API format
- [ ] `check_deps.py` reports `is_ready: true` (or only `node_check_skipped`
      on cloud free tier)
- [ ] Test run with a small workflow completes; outputs land in `--output-dir`

---

*Ported to hakase from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (MIT). Original: Hermes Agent creative skill authored by kshitijk4poor, alt-glitch, and purzbeats (MIT). Underlying tool: [ComfyUI](https://github.com/comfyanonymous/ComfyUI) (GPL-3.0) by comfyanonymous.*
