# hakase Knowledge Base - Reference

## Location & layout

Default: `./knowledge` (config `knowledge_dir`; `~` expands to home). Layout:

```
knowledge/
├── index.md    # auto-maintained catalog, regenerated on every change
├── log.md      # append-only operation log: "## [date] action | Title"
├── notes/      # optional subdirectory (preferred when a slug exists in both places)
└── raw/        # optional immutable raw sources (excluded from the index)
```

## Note format

Each note is markdown with YAML frontmatter:

```yaml
---
title: Note Title
aliases: [alt-name]
tags: [tag1, tag2]
created: 2026-01-01
updated: 2026-01-02
status: permanent        # draft | permanent | archived
confidence: high          # high | medium | low
sources: [https://...]    # URLs or raw/ paths
summary: One-line summary
related: [other-note]
---
Body with [[wikilinks]] to related notes.
```

## The 8 tools

| Tool | Purpose |
|---|---|
| `save_knowledge` | save a new note; unresolved wikilinks reported as dangling |
| `recall_knowledge` | load a note by slug/basename/alias, with backlinks |
| `search_knowledge` | keyword/tag grep across notes |
| `update_knowledge` | correct or extend an existing note |
| `link_knowledge` | create [[wikilinks]] between notes |
| `cite_knowledge` | footnote-style citation of a note with its source URL |
| `list_knowledge` | list all notes |
| `lint_knowledge` | health check: orphans, dangling links, broken index |

## Wikilinks

- `[[target]]`, `[[target|label]]`, `[[target#heading]]`.
- Resolution is case-insensitive: slug -> unique basename -> alias.
- Links to notes that do not exist yet are **dangling**; the agent MUST surface them
  to the user, list the missing notes, and offer to create them - creating only after
  user confirmation.

## Operational rules

- Durable facts, preferences, and decisions the user shares should be saved
  proactively (`save_knowledge`); before answering about a known topic, `recall_knowledge`
  first to ground the reply.
- When the user asks where a previously produced artifact lives, check the task board
  (`list_tasks`/`get_task`) and knowledge (`search_knowledge`/`recall_knowledge`) BEFORE
  searching the filesystem.
- Retrieval is keyword/tag/grep only - no embeddings, no vector database, no extra
  dependencies.
- Notes are the source of truth: there is no separate registry.

## CLI

```
hakase knowledge list|read|search|lint|create|link [--dir <path>]
```

Example:

```bash
hakase knowledge create "Quantum Computing" --tags physics --content "See [[Superposition]]."
hakase knowledge read quantum-computing
hakase knowledge lint
```

## User-global knowledge

Set `knowledge_dir: "~/.hakase/knowledge"` (or `$HAKASE_HOME/knowledge`) so durable
facts persist across all projects on the machine.
