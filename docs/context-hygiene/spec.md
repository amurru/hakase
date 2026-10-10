# Spec: Context Hygiene — lazy skills at scale, KB caps, durable pins

Governing issue: TBD (Tier-3 proposal, P0-2). Reference: Pi lazy skills, agentskills.io 3-stage disclosure, MCP gateway search pattern, Bouchard caps-38%-free, Loop&Retry pins.

**Scope:** scale skill index past ~100, bound KB/tool outputs, pin hard constraints, keep cache stable. Non-goals: behavior change under threshold, new deps, KB redesign.

## Decisions

- **Lazy loading already exists, keep it.** `getSkillsPrompt` (`internal/agent/agent.go:1109`) emits one line per skill; `load_markdown_skill` (`agent.go:1215`) loads on demand; `list_skills` (`agent.go:1056`) is the tool index. Guard tests (`skills_index_size_test.go`) pin overhead + 64KB budget. P0-2 extends, not rebuilds.
- **Threshold-gated stub + search.** Under threshold keep eager index (zero extra round-trips). Over threshold (e.g. >50 skills or 8-12KB) replace with stub + new `search_skills{query,limit}`. Mirrors Pi-orchestrator `skill_search`→`skill` and gateway `search_tools`→`execute_tool`.
- **KB already jailed, bound it.** `search_knowledge` returns Title/Slug/Summary/Tags/Updated/Status + query-aware snippet (`FirstSnippet`, `internal/knowledge/knowledge_tools.go:262,913`; `snippetWindow = 200` at `:905`). Body is never returned. Add count/byte caps, keep `recall_knowledge` as explicit full-load gate.
- **Stable caps beat summaries for cache.** Per-tool-output cap at persist time (deterministic marker) does not rewrite cached prefix; summaries do. Keep existing cascade (`internal/context/context.go:532 fitToBudget`, `:627 StageBSnip`, `internal/context/summarize.go:65`), add cap tier first + durable-pin bin.
- **Copy the date pattern.** `buildTimeReminder` day-keyed (`agent.go:349`, day key `agent.go:311,375`) is the cache-breaker guard template. New dynamic blocks must be day-or-coarser or post-prefix messages, never system-prefix interpolation.

## Specs

### Spec CX-001: index clamp + stub + search

- Clamp each description ~200-300 chars at word boundary. Flat per-skill budget already rejected in test comment; clamp is per-description bound.
- Stub text when over threshold: `N skills installed; call search_skills{query}`.
- `search_skills{query, limit=5}`: substring/BM25 over name+description only (reuse `hctx.Tokenize` + `score.go`), returns name + full description, nothing else. Same construction as `CreateLoadMarkdownSkillTool` (index map + one fresh re-scan on miss), read-only, no approval, audit-logged. Register in both tool lists (`agent.go:2364,2514`).

### Spec CX-002: KB caps

- `search_knowledge`: max 10 results / 2KB total snippets, truncate with `...[N more, refine query]`. `SearchOptions` (`internal/knowledge/knowledge_tools.go:290`) carries caps alongside Expansion/Hybrid flags.

### Spec CX-003: tool-output cap + durable pins + lint

- Cap 4-8KB deterministic truncation at persist time with marker.
- Durable-pin bin: constraints/IDs/goal/decisions exempt from `StageBSnip`/summarizer; reconstruction test (rebuild ruled-out + never-do from compacted context alone).
- Description lint at discovery: warn on empty/vague (<20 chars, no verb). Body-line-count lint + `references/` convention in `internal/skill/skill_discovery.go:38` / `internal/skill/skills_md.go:40`.
- Reserve accounting (`internal/context/context.go:84-87,554-555`) must include skill-index tokens (currently ctx+git+env only). Eviction log (what dropped, when).

### Spec CX-004: config (default-off precedent)

- `skills_index_mode` (eager vs search-stub auto), `skills_search_threshold`, `tool_output_max_chars`, `context_durable_pins`. Follow the `hybrid_search` byte-identical-when-off precedent (`config.json.example:49`; `search_expansion` lives only in `config.go:218`).

## Non-goals

- Eager-to-lazy migration under threshold, embedding rerank for skill search (keyword first), AGENTS.md total-ceiling redesign (same hygiene applies, separate ticket).

## Definition of done

- [ ] Repo skill tree (23 own `SKILL.md`, ~148 with editor/agent dirs per `skills_index_size_test.go:16-18`) byte-identical under threshold; stub + search over threshold.
- [ ] KB search bounded; pin survival test passes; prefix byte-stable across turns.
- [ ] Suite green.

## Validation against codebase (2026-10-08)

Already exists, do NOT rebuild: one-line index (`agent.go:1109,1173`), loader tool (`agent.go:1215-1263`), list tool (`agent.go:1056`), orchestrator `SKILL REUSE` (`agent.go:1918`), code_interpreter wiring (`agent.go:2347`), discovery order + validation (`internal/skill/skill_discovery.go:38`, `internal/skill/skills_md.go:40`), instruction assembly (`agent.go:2002`, `internal/context/instruction_context.go:124,312`), compaction cascade (`internal/context/context.go:532,627`, `internal/context/summarize.go:65`), jailed KB snippets, injection sanitization, day-keyed date.
Note: "do NOT rebuild" means do not add a second function doing the same thing (NO SLOP). Extending or enhancing the listed paths in place is fine and expected.
To build: `search_skills` tool (grep 0 hits), description clamp + stub threshold, KB count/byte caps, persist-time tool-output cap, durable-pin bin, reserve accounting for skill index, description/body lint, eviction log.
