# Research Skills Port - Shared Conventions

Reference for porting Hermes Agent research skills into hakase's markdown
skill system. Every ported skill MUST follow these conventions. This is the
sibling of `.agents/skills/creative/_CONVENTIONS.md`; it reuses the same
frontmatter/attribution rules and adds the research-specific tool mapping.

## Frontmatter template

```yaml
---
name: <kebab-case-name>          # MUST equal the directory name
description: '<trigger-rich, <=1024 chars, "Use when the user asks...">'
license: MIT
metadata:
  author: '<TRUE ORIGINAL AUTHOR(S); hakase ported via Hermes Agent (MIT)>'
  version: 1.0.0
  source: https://github.com/NousResearch/hermes-agent/tree/main/optional-skills/research/<name>
allowed-tools: read_file, write_file, patch, search_files, system_exec, python_interpreter, load_markdown_skill, delegate_task
---
```

Rules:
- Strip Hermes-only frontmatter fields (`platforms`, `dependencies`,
  `version`/`author` at top level, `metadata.hermes.*`, `prerequisites`,
  `fallback_for_toolsets`) - fold provenance into `metadata.source` + the
  attribution footer.
- **ATTRIBUTION (mandatory):** `metadata.author` MUST name the true original
  author/creator of the skill (or its underlying library when the skill wraps
  one), with Hermes listed only as the porting intermediary. Do NOT shadow
  upstream authors behind "ported from Hermes". Source of truth: the
  `author`/`homepage`/`adapted from` lines in the Hermes source SKILL.md.
  Quote the value in single quotes (author strings often contain `: `).
- Description: first sentence = what it does + "Use when..." trigger phrases.
  <=1024 chars enforced by hakase.
- Keep upstream attribution in the body footer.

## `allowed-tools` (informational - use real hakase tool names only)

hakase parses `allowed-tools` but does NOT enforce it (no gating). Keep it
accurate anyway: list ONLY tool names that exist in hakase, so readers and
agents can trust it. Canonical hakase tool names:

| Tool | Where it lives |
|---|---|
| `read_file`, `write_file`, `patch`, `search_files` | orchestrator + general_purpose |
| `system_exec`, `system_exec_start`, `system_exec_status`, `system_exec_list`, `system_exec_kill` | orchestrator (system_exec family) |
| `python_interpreter` | code_interpreter SUB-AGENT only - reachable from the orchestrator via `delegate_task` (agent_name=code_interpreter) |
| `load_markdown_skill`, `list_skills`, `save_skill` | orchestrator (`load_markdown_skill`, `list_skills`) + code_interpreter (`save_skill`) |
| `delegate_task` | orchestrator |
| `cronjob`, `clarify` | orchestrator |
| `vision` | orchestrator + all sub-agents |
| `save_knowledge`, `recall_knowledge`, `search_knowledge`, `update_knowledge`, `link_knowledge`, `cite_knowledge`, `list_knowledge`, `lint_knowledge` | orchestrator |
| `download_file` | web_researcher sub-agent |

Base template for doctrine skills (matches what the shipped research skills
use): `read_file, write_file, patch, search_files, system_exec,
python_interpreter, load_markdown_skill, delegate_task`. Add per-skill tools
when the body actually invokes them - e.g. `darwinian-evolver` adds
`cronjob, save_skill`; `bioinformatics` adds `vision`. Do NOT invent tool
names that do not exist in hakase.

## Tool-name mapping (Hermes -> hakase)

Apply to EVERY skill body:

| Hermes | hakase equivalent |
|---|---|
| `terminal` | `system_exec` |
| `execute_code` | `python_interpreter` (runs in the `.venv`; skills are prompt-discovered, no wiring needed) |
| `skill_view(file_path=...)` | `read_file` on the bundled file (`scripts/...`, `references/...`, `templates/...` relative to the skill dir) |
| `web_search` / `web_extract` toolset refs | STRIP. Generic web research is covered by the `web_researcher` sub-agent (Lightpanda MCP browser + deepwiki). Where a comparison table referenced `web_search`/`web_extract`, replace with `delegate_task` to `web_researcher`. |
| `browser_*` verification | `delegate_task` to `web_researcher` (Lightpanda), OR a documented manual step when verification is optional |
| `done()` / `show_html()` / `eval_js_user_view()` / hosted-tool plumbing | delete - Hermes-hosted session calls, meaningless in hakase |
| `fallback_for_toolsets` | delete - keyless search-fallback skills were dropped (redundant with web_researcher) |
| Artifacts / `data/` / `~/...` outputs | `./outputs/` (hakase convention) |

Notes:
- hakase has NO native `web_search` tool. The `web_researcher` sub-agent
  (via `delegate_task`) is the browser/search surface.
- CLI tools invoked via `system_exec` must be installed in trusted dirs
  (`/usr /lib /lib64 /bin /sbin /etc /nix /proc /dev /sys /tmp /run`) or the
  skill body must include the install command.
- `SKILL_DIR` references in upstream bodies stay valid: they mean "the
  directory containing this SKILL.md".
- Env vars prefixed `HERMES_*` are renamed to `HAKASE_*` in skill bodies
  (e.g. `HERMES_OSINT_UA` -> `HAKASE_OSINT_UA`); the change is documented in
  the manifest.

## Dependency policy

- Paid services are OUT OF SCOPE (`parallel-cli` dropped).
- Free/open-source 3rd-party deps (pip packages, public REST APIs) are
  acceptable. Document the prerequisite in the body (like the creative port's
  manim/design-md).
- Tier 2 skills gate execution on an availability check (`python3 -c "import
  scrapling"` or `command -v <tool>`).
- Bubblewrap sandbox note: skills that need outbound network must document
  `allow_network: true` for the `bubblewrap` sandbox mode.

## Output convention

Generated artifacts and downloaded data go to `./outputs/`. Update any
"save to data/..." or "save to ~/..." instructions to use `./outputs/`.

## Attribution footer

Every SKILL.md ends with a footer crediting provenance, naming the TRUE
original author/creator:

```
---

*Ported to hakase from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (MIT).
Original: <original author/repo description> by <original author> (<license>).*
```

## Directory structure

```
.agents/skills/<name>/          # NOTE: top-level, NOT .agents/skills/research/<name>/
  SKILL.md                      # frontmatter + adapted body
  scripts/                      # helper scripts (only if genuinely useful at runtime)
  references/                   # deep-dive docs (kept as-is, read via read_file)
  templates/                    # starter files (kept as-is)
```

Deviation from the plan's `.agents/skills/research/<name>/` layout:
discovery (skill_discovery.go) scans exactly ONE directory level for
directories containing SKILL.md, so a nested `research/<name>` would never be
discovered (breaking `hakase skill list`). Following the creative-port
precedent, ported skills live at `.agents/skills/<name>/` (top level) and
this `research/` directory holds only the shared conventions + porting
manifest. Zero core-code change needed.

Do NOT port: Hermes-only test dirs, README.md duplicates (fold key info into
SKILL.md), hosted-tool plumbing.

## Validation

Every skill must pass:
```bash
hakase skill validate .agents/skills/<name>
```
Exit 0 required. Grep gate: no `skill_view`, `web_search`/`web_extract`
toolset refs, `fallback_for_toolsets`, `done()`, or `show_html()` in any body.
