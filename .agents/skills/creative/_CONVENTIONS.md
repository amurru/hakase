# Creative Skills Port - Shared Conventions

Reference for porting Hermes Agent creative skills into hakase's markdown skill
system. Every ported skill MUST follow these conventions.

## Frontmatter template

```yaml
---
name: <kebab-case-name>          # MUST equal the directory name
description: '<trigger-rich, <=1024 chars, "Use when the user asks...">'
license: MIT
metadata:
  author: '<TRUE ORIGINAL AUTHOR(S); hakase ported via Hermes Agent (MIT)>'
  version: 1.0.0
  source: https://github.com/NousResearch/hermes-agent/tree/main/skills/creative/<name>
---
```

Rules:
- Strip Hermes-only frontmatter fields (`platforms`, `dependencies`,
  `metadata.hermes.*`, `version`/`author` at top level) - fold provenance into
  `metadata.source` and the attribution footer.
- **ATTRIBUTION (mandatory):** `metadata.author` MUST name the true original
  author/creator of the skill (or its underlying library/format when the skill
  itself is original to Hermes), with Hermes listed only as the porting
  intermediary. Do NOT shadow upstream authors behind "ported from Hermes".
  Quote the value in single quotes - author strings often contain `: ` which
  breaks YAML parsing. Source of truth: the `author`/`homepage` fields and
  "Based on / Adapted from / ported from" lines in the Hermes source SKILL.md,
  verified against the upstream repo.
- Description: first sentence = what it does + "Use when..." trigger phrases.
  <=1024 chars enforced by hakase.
- Keep upstream attribution in the body footer (see Attribution section).

## Tool-name mapping (Hermes -> hakase)

Apply to EVERY skill body:

| Hermes | hakase equivalent |
|---|---|
| `image_generate` | Per-skill adaptation. Default: build self-contained HTML/SVG output via `write_file`. Optional: external image API via `system_exec` curl (requires user key + network). |
| `skill_view(name=..., file_path=...)` | `read_file` on the bundled file (path `references/...` or `templates/...` relative to the skill dir) |
| `write_file` / `read_file` / `patch` | unchanged (exist on orchestrator) |
| `browser_*` (navigate/vision/click) | `delegate_task` to `web_researcher` (Lightpanda MCP) for verification, OR documented manual step ("open the file in your browser") when verification is optional |
| `terminal` | `system_exec` |
| `clarify` | unchanged (exists on orchestrator) |
| `done()` / `show_html()` / `eval_js_user_view()` / hosted-tool plumbing | delete - these are Hermes-hosted session calls, meaningless in hakase |

Notes:
- hakase has NO native image/video/audio generation tools. Any skill that
  relied on `image_generate`/`video_generate`/`tts` must be adapted (Tier 3).
- Browser automation only exists on `web_researcher` (Lightpanda MCP). The
  orchestrator cannot drive a browser directly; use `delegate_task`.
- CLI tools invoked via `system_exec` must be installed in trusted dirs
  (`/usr /lib /lib64 /bin /sbin /etc /nix /proc /dev /sys /tmp /run`) or the
  skill body must include the install command.

## Output convention

Generated artifacts go to `./outputs/` (hakase convention). Update any
"save to ~/..." or "save to cwd" instructions to use `./outputs/`.

## Attribution footer

Every SKILL.md ends with a footer crediting provenance. The footer MUST name
the true original author/creator, NOT just Hermes:

```
---

*Ported to hakase from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (MIT).
Original: [<original author/repo>](<url>) by <original author> (<license>).*
```

For skills that are original Hermes creations wrapping an external library or
format (e.g. p5js, manim-video, pretext, excalidraw, design-md), credit both:
"Skill by Hermes Agent (MIT). Underlying library/format: <name> by <author>
(<license>)."

Keep any upstream LICENSE file bundled (e.g. humanizer's LICENSE).

## Directory structure

```
.agents/skills/<name>/
  SKILL.md            # frontmatter + adapted body
  scripts/            # helper scripts (only if genuinely useful at runtime)
  references/         # deep-dive docs (kept as-is, read via read_file)
  templates/          # starter files (kept as-is)
```

Do NOT port: Hermes-only test dirs, README.md duplicates (fold key info into
SKILL.md), hosted-tool plumbing.

## Validation

Every skill must pass:
```bash
hakase skill validate .agents/skills/<name>
```
Exit 0 required.
