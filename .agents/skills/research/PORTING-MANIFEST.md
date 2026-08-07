# Research Skills Port - Porting Manifest

Status of the research skill port from Hermes Agent
(`NousResearch/hermes-agent`, `optional-skills/research/`) into hakase, plus
the self-evolution and knowledge-search quality workstreams. See
`.omo/plans/research-skills-port.md` for the full plan.

## Scope decisions (from plan review, revs 2-4)

Dropped skills and why (web_researcher coverage analysis, plan section 4):

| Dropped | Reason |
|---|---|
| `duckduckgo-search` | Redundant - web_researcher (Lightpanda browser) can reach a search engine and extract results; the skill only adds keyless resilience when Lightpanda is down |
| `searxng-search` | Redundant - same coverage |
| `parallel-cli` | Paid dependency (FindAll); plan requires free/open-source only |
| `qmd` | External skill (Node 22+ + local LLM) not ported; retrieval ideas folded into hakase core as Phase 3d (BM25 ranking, index caching, query-aware snippets, LLM query expansion, `hakase knowledge bench`) |
| `gitnexus-explorer` | User follows the MCP route instead (install GitNexus locally, add to `config.json` `mcp.servers`; zero code, out of plan scope) |
| `pinecone-research` | Not in the reviewed plan scope; knowledge base explicitly rejects vector DB / embeddings (plan non-goal). Left out; revisit only if the user asks |

Port scope: **6 skills** (`domain-intel`, `osint-investigation`,
`drug-discovery`, `bioinformatics`, `scrapling`, `darwinian-evolver`).

## Layout deviation (documented)

The plan's `.agents/skills/research/<name>/` layout is NOT used for the skill
directories: discovery (skill_discovery.go) scans exactly one directory level
for dirs containing SKILL.md, so a nested `research/<name>` would never be
discovered. Skills live at `.agents/skills/<name>/` (top level, same as the
creative port); this `research/` dir holds only conventions + manifest.
Zero core-code change.

## Ported skills

| Skill | Tier | Original author (verified) | Status | Validate | Smoke | Notes |
|---|---|---|---|---|---|---|
| domain-intel | 1 | FurkanL0 (Contributed by @FurkanL0); Hermes Agent (MIT) | done | PASS | PASS | doctrine + scripts/domain_intel.py (stdlib-only: crt.sh CT logs, WHOIS TCP, DoH, SSL, bulk); terminal -> system_exec; web_search/web_extract refs -> delegate_task/web_researcher; live smoke: dns + whois on example.com OK (crt.sh subdomains timed out - documented external-service slowness) |
| osint-investigation | 1 | Hermes Agent, adapted from ShinMegamiBoson/OpenPlanter (MIT) | done | PASS | PASS | follow-the-money doctrine; 11 source references + 16 stdlib scripts + source template; terminal -> system_exec; arxiv/sherlock cross-refs stripped; HERMES_OSINT_* -> HAKASE_OSINT_* (2 scripts); all scripts py_compile clean |
| drug-discovery | 1 | bennytimz (MIT) | done | PASS | PASS | ChEMBL/OpenFDA/OpenTargets/PubChem doctrine; scripts chembl_target.py + ro5_screen.py; references/ADMET_REFERENCE.md; curl/python3 -> system_exec; scripts py_compile clean |
| bioinformatics | 1 | Hermes Agent (MIT) | done | PASS | PASS | gateway doctrine (bioSkills 385 + ClawBio 33, cloned on demand - never committed); no scripts; system_exec note + bubblewrap allow_network note |
| scrapling | 2 | FEUAZUR (MIT); library: D4Vinci/Scrapling (MIT) | done | PASS | PASS | 4-fetch-strategy doctrine; prerequisite `pip install scrapling[all]` documented; bubblewrap `allow_network: true` note; availability gate; web_extract ref -> web_researcher |
| darwinian-evolver | 3 | Hermes Agent (MIT); upstream: imbue-ai/darwinian_evolver (AGPL-3.0, NOT imported) | done | PASS | PASS | reimplemented natively in Go (evolver.go); SKILL.md documents the native evolution layer (hakase skill evolve CLI + native cron job), NOT the AGPL upstream |

Legend: Tier 1 = doctrine + stdlib, Tier 2 = doctrine + free/OSS external dep,
Tier 3 = core Go engine (plan section 5). Validate = `hakase skill validate`
result. Smoke = live API / syntax / cycle verification result.

## Phase 3 - self-evolution + knowledge-search quality

| Workstream | File(s) | Status | Notes |
|---|---|---|---|
| 3a Reflexion loop | agent.go (orchestrator instruction) | done | post-task reflection -> save_knowledge lessons notes after failed/complex tasks |
| 3b Evolver engine | evolver.go, evolver_test.go, skill_cli.go | done | organism/evaluator/mutator/selection/driver over skills/skills.json; no AGPL import |
| 3c A/B gate | evolver.go | done | promote >=5% better + zero regressions; eval hit-rate tracked; skills <30% auto-deprecated |
| 3d-1 BM25 ranking | tokenize.go, score.go, knowledge.go | done | score-descending order, alphabetical tiebreak; same result set |
| 3d-2 Index caching | knowledge.go, knowledge_tools.go | done | fingerprint-invalidated in-memory cache (path/size/mtime) |
| 3d-3 Query-aware snippets | knowledge_tools.go | done | snippet centered on first match |
| 3d-4 LLM query expansion | knowledge.go, config.go | done | `search_expansion` bool default false; silent fallback |
| 3d-5 `hakase knowledge bench` | knowledge_cli.go, score.go | done | recall@k / MRR on shared eval set |

## Acceptance criteria status

- [x] All ported skills validate (exit 0) - all 6 PASS
- [x] `hakase skill list` discovers all ported skills after restart - all 6 listed
- [x] Grep gate: no Hermes-only tool names in bodies - clean (one `web_extract` in osint ref doc fixed)
- [x] Dropped skills documented above with rationale
- [x] NO PAID DEPENDENCIES anywhere (parallel-cli out of scope)
- [x] Smoke tests pass - domain-intel live (dns/whois), evolver full cycle (unit + CLI), knowledge bench (recall@k 1.00/MRR 1.000 on smoke set), cron native evolve contract
- [x] `search_knowledge` returns same result set, relevance-ranked - unit-tested (TestSearchKnowledge_ResultSetUnchanged)
- [x] `hakase knowledge bench` exists, reports recall@k / MRR - verified live
- [x] `search_expansion` default OFF; byte-identical behavior when off - config field default false, tested
- [x] Only new config field: `search_expansion` (plus HAKASE_SEARCH_EXPANSION env)
- [x] README + hakase self-skill updated

## `allowed-tools` review (post-port, 2026-08-07)

`allowed-tools` in the frontmatter is parsed but NOT enforced by hakase
(only the struct field in skills_md.go references it; the agent prompt does
not render it). It is kept accurate anyway. All names in the base template
(`read_file, write_file, patch, search_files, system_exec,
python_interpreter, load_markdown_skill, delegate_task`) are real hakase
tools; `python_interpreter` lives on the code_interpreter SUB-AGENT
(reachable from the orchestrator via `delegate_task`), which is noted in
_Conventions.md. Two skills were updated for accuracy:
- `darwinian-evolver` adds `cronjob, save_skill` (its body documents
  creating the native evolution job and rewriting skills via save_skill).
- `bioinformatics` adds `vision` (the gateway content steers toward
  vision-using ClawBio pipelines).
The canonical hakase tool-name table now lives in `_CONVENTIONS.md`.

## Validation log

Filled in Phase 4 of the plan; each skill row above carries its final
validate/smoke result. All 6 skills: `hakase skill validate` exit 0, grep
gate clean, discovered by `hakase skill list`. Full `go test ./...` suite
green (6.6s) including new score_test.go + evolver_test.go + cron native
contract test. Smoke details:
- domain-intel: `dns example.com` (live A/AAAA/MX/NS) + `whois example.com`
  (live registrar data) PASS; `subdomains` via crt.sh timed out (documented
  "crt.sh can be slow" caveat, external service).
- osint/drug-discovery scripts: all py_compile PASS.
- evolver: full mutate -> eval -> select promotion, no-gain rejection,
  parse-failure no-op, deprecation, .bak rollback - all unit-tested;
  CLI `hakase skill evolve` runs evaluation-only pass + writes report.
- knowledge bench: recall@k 1.00 / MRR 1.000 on the 2-query smoke set.
- cron native: `native: "evolve"` create contract unit-tested (accepts
  evolve without prompt, rejects unknown types).
