# Creative Skills Port - Porting Manifest

Status of each ported skill from Hermes Agent
(`NousResearch/hermes-agent`, `skills/creative/`) into hakase.

All 15 skills validated via `hakase skill validate` (exit 0), discovered via
`hakase skill list`, and grep-gated (no Hermes-only tool invocations).
Smoke tests: API paths live (wttr.in, api.github.com/octocat), all ported
scripts pass syntax checks (py_compile / bash -n / node --check), adaptation
points verified in body.

| Skill | Tier | Original author (verified) | Status | Validate | Smoke | Notes |
|---|---|---|---|---|---|---|
| humanizer | 1 | Siqi Chen (blader) - blader/humanizer (MIT) | done | PASS | PASS | pure doctrine; LICENSE bundled |
| claude-design | 1 | BadTechBandit (MIT) - original Hermes skill | done | PASS | PASS | doctrine + HTML; hosted-plumbing list kept as ignore/remap |
| sketch | 1 | Lex Christopherson - GSD /gsd-sketch (MIT, archived) | done | PASS | PASS | HTML variants; browser -> delegate_task/web_researcher or manual |
| excalidraw | 1 | skill: Hermes; format: Christopher Chedeau (Vjeux) / Excalidraw (MIT) | done | PASS | PASS | JSON schema; upload.py ported (py_compile OK) |
| architecture-diagram | 1 | Cocoon AI - architecture-diagram-generator (MIT) | done | PASS | PASS | SVG/HTML; skill_view -> read_file |
| popular-web-designs | 1 | skill: Hermes + Teknium; designs: VoltAgent/awesome-design-md (MIT) | done | PASS | PASS | 54 templates copied; browser_vision -> manual/delegate |
| songwriting-and-ai-music | 1 | Hermes Agent (MIT) - original Hermes creation | done | PASS | PASS | doctrine; Suno external (noted) |
| ascii-art | 1 | 0xbyt4 + Hermes Agent (MIT) - original Hermes skill | done | PASS | PASS | CLI + free APIs; wttr.in/octocat verified live |
| p5js | 2 | skill: Hermes; library: Lauren Lee McCarthy / Processing Foundation - p5.js (LGPL-2.1) | done | PASS | PASS | 16 files; CDN + browser; render.sh/export-frames.js syntax OK |
| pretext | 2 | skill: Hermes; library: Cheng Lou - @chenglou/pretext (MIT) | done | PASS | PASS | CDN + browser; templates + patterns.md copied |
| design-md | 2 | skill: Hermes; spec/tool: Google - google-labs-code/design.md (Apache-2.0) | done | PASS | PASS | npx CLI; Node prerequisite documented |
| ascii-video | 2 | Hermes Agent (MIT) - original Hermes creation | done | PASS | PASS | 9 files; 8 references copied; prerequisites documented |
| manim-video | 2 | skill: Hermes; library: Grant Sanderson (3Blue1Brown) + Manim Community - Manim CE (MIT) | done | PASS | PASS | 16 files; 14 references + setup.sh; LaTeX prereq documented |
| baoyu-infographic | 3 | JimLiu (宝玉) - JimLiu/baoyu-skills (MIT) | done | PASS | PASS | ADAPTED: HTML/SVG output, 46 files (21 layouts x 21 styles) |
| comfyui | 3 | kshitijk4poor, alt-glitch, purzbeats (MIT) - original Hermes skill | done | PASS | PASS | doctrine + infra gate; 25 files; tests/ NOT ported |
| touchdesigner-mcp | skipped | Hermes original | skipped | - | - | needs orchestrator MCP client; see README TODO |

Legend: Validate = `hakase skill validate` result. Smoke = live API / syntax /
adaptation verification result.

Deferred (per README TODO): revisit touchdesigner-mcp after orchestrator MCP
client support; revisit baoyu-infographic native image backend + comfyui +
songwriting TTS after image_gen / video_gen / audio tools land.
