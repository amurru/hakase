# hakase Skills - Authoring Guide

## The two skill types

1. **Markdown skills** (`SKILL.md` directories) - portable, agent-agnostic, the modern
   format. Interoperable with Claude Code, Codex, Gemini CLI, OpenCode (agentskills.io).
2. **Python skills** (`./skills/*.py` + `skills/skills.json`) - legacy self-evolved
   scripts saved by `save_skill` after verified execution. Import via
   `from skills.<name> import ...`.

On name collision the markdown skill wins in the prompt; the `.py` stays importable.

## SKILL.md format

```
<skill-name>/
├── SKILL.md       # required: YAML frontmatter + progressive-disclosure body
├── scripts/       # optional: executable code files
└── references/    # optional: deeper docs, loaded on demand
```

### Frontmatter

```yaml
---
name: my-skill            # required, ^[a-z0-9]+(-[a-z0-9]+)*$, <=64 chars
description: '...'        # required, <=1024 chars - what triggers loading
license: MIT              # optional
compatibility: ...        # optional
metadata:                 # optional, arbitrary map
  author: ...
  version: 1.0.0
allowed-tools: ...        # optional
---
```

Rules enforced by `ParseMarkdownSkill`:
- Frontmatter must start at byte 0 with `---\n`; the FIRST line whose trimmed content
  is exactly `---` closes it (a `---` in the body is not a delimiter).
- Directory name must EXACTLY match frontmatter `name` (case-sensitive).
- Description must be non-empty and <=1024 chars.
- A UTF-8 BOM is tolerated; CRLF normalized.
- Metadata parsing is tolerant: a bad metadata value falls back to name+description
  only (the skill survives).

### Description: write for the trigger

The description is what the model matches against. Be specific about when to use the
skill: trigger phrases, contexts, and what the skill knows. Bad: "General helper".
Good: "Knowledge about the X agent itself - use when the user asks who you are, how to
configure X, or how to extend it."

## Discovery (first match by name wins)

1. Project walk (cwd up to git root, nearest first): `.agents/skills`, `.claude/skills`,
   `.opencode/skills`, `.gemini/skills`
2. Project library: `<root>/skills`
3. Custom: `skill_dirs` from config (relative -> resolved against project root)
4. User level: `~/.hakase/skills` (or `$HAKASE_HOME/skills`), `~/.agents/skills`,
   `~/.claude/skills`, `~/.gemini/skills`, `~/.config/opencode/skills` (XDG-aware)

Rules:
- Skills placed OUTSIDE these paths are never loaded.
- Invalid skills are skipped with a logged warning (never fail discovery).
- Skills added mid-session require a restart to be discovered (one lazy re-scan
  happens on `load_markdown_skill` with an unknown name).
- `.agents/skills/` is meant to be committed to the repository.

## Prompt integration

At startup, all discovered skills are listed under "AVAILABLE PRE-LEARNED SKILLS" in
the orchestrator and code_interpreter instructions, each with its discovery source
directory. The body is NOT inlined - the agent calls `load_markdown_skill` to read the
full body on demand. Keep the essential knowledge in the body itself; put deep dives in
`references/`.

## CLI

```
hakase skill create <name> [--dir <path>] [--description <text>] [--template python] [--force]
hakase skill list
hakase skill validate <dir>
```

`skill create` scaffolds `<dir>/<name>/SKILL.md` with valid frontmatter
(`name`, `description`, `license: MIT`, `metadata: author/version`) plus `scripts/`
and `references/`. Default dir: `<projectRoot>/.agents/skills/`.
`--template python` also writes `scripts/<name>.py`.

## Installing skills from GitHub

hakase has no built-in fetch command yet (create/list/validate only). The ecosystem
standard is `gh skill install <owner/repo>` which writes project-scope skills to
`.agents/skills/` - compatible with hakase discovery. A skill fetched this way is
picked up after restart. For user-scope installs, place skills in
`~/.hakase/skills/` (or `~/.agents/skills/`).
