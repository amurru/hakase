# Execution Plan: Rich Markdown Rendering in the Web UI

Feature: `markdown-rendering`
Source of truth: `spec.md` (atomic specs MD-001..MD-008). This file sequences
the work into phases with owners, exit criteria, and parallelization.

## Phases

### Phase 0 - Decisions (blocking, before Phase 2 backend)
- [x] Decide CSP media policy for MD-007:
      - Default (recommended): `media-src 'self' data:`; external media and
        oembed iframes render as styled links.
      - Alternative A: add `https:` to `media-src` (external video/audio plays).
      - Alternative B: also add `frame-src https:` (YouTube/Vimeo embeds).
      **RESOLVED (2026-08-12): default.** `media-src 'self' data:`; no oembed,
      no `frame-src`. Recorded in `research.md`.
- [x] Decide whether MD-006 v1 needs HTTP Range support (seekable video) or
      whole-file streaming is enough.
      **RESOLVED (2026-08-12): whole-file streaming; no Range in v1.**
      Seekable video deferred to v2. Recorded in `research.md`.

### Phase 1 - Frontend foundation (serial)
**MD-001** Pipeline factory + DOMPurify config (`webui/src/lib/markdown.ts`).
- Exit: `renderMarkdown()` exists; `katex` + `@vscode/markdown-it-katex` in
  `package.json`; basic + math + sanitization tests pass.

### Phase 2 - Parallel tracks (after MD-001; backend tracks can start now)
Run these five tracks concurrently:

- **Track A (FE): MD-002** highlight.js integration + theme swap.
- **Track B (FE): MD-003** media-link markdown-it plugin.
- **Track C (FE): MD-004** mermaid placeholder + `useMermaid` hydration.
- **Track D (BE): MD-006** `GET /api/files/inline` route + tests.
- **Track E (BE): MD-007** CSP `media-src` (per Phase 0 decision) + tests.

Exit per track: the track's acceptance criteria + its slice of MD-008.

### Phase 3 - Integration (after A + B + C)
**MD-005** Wire pipeline into `MarkdownRenderer.vue`; add copy-on-code and
external-link hardening; verify streaming behavior.
- Exit: a live agent response with math + a mermaid diagram + an image renders
  correctly end-to-end; no console errors during streaming.

### Phase 4 - Hardening (after integration + D + E)
**MD-008** Full test suite + `fixtures.md`.
- Exit: `pnpm test` green; `go test ./...` green; manual QA matrix in
  `tasks.md` signed off.

## Critical Path

```
MD-001 ──┬─► MD-002 ──┐
         ├─► MD-003 ──┼─► MD-005 ─► MD-008
         └─► MD-004 ──┘                ▲
MD-006 ──────────────────────────────┤
MD-007 ──────────────────────────────┘
```

- MD-001 is the sole frontend serial prerequisite.
- MD-006/007 are independent and can start immediately.
- The longest path is MD-001 -> (002|003|004) -> 005 -> 008.

## Suggested Task Sizing (1-3 tool calls each)

See `tasks.md` for the atomic, hand-offable task list. Each task is sized so it
can be completed by editing one or two files (or one Go file) and running one
verification command.

## Definition of Done (feature-level)

1. A streaming agent response containing: headings, a fenced code block, a
   table, inline `$...$` math, display `$$...$$` math, a ` ```mermaid `
   diagram, a workspace image, and a workspace `.mp4` - all render correctly in
   the browser with no console errors and no CSP violations.
2. `pnpm test` and `go test ./...` are green.
3. `make build` produces a binary whose embedded SPA includes the KaTeX fonts
   and the code-split mermaid chunk.
4. CSP `script-src 'self'` is unchanged; sanitization blocks `<script>` and
   inline event handlers on every path.
5. `docs/markdown-rendering/fixtures.md` captures the verified before/after.

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| Mermaid throws during streaming (partial source) | High | Medium | try/catch + fallback `<pre>`; render-cache keyed on source |
| DOMPurify strips KaTeX MathML / styles | Medium | High | `USE_PROFILES:{html,mathMl}` + `ADD_ATTR:['style']`; test in MD-008 |
| KaTeX fonts blocked by CSP | Low | High | bundle via `katex.min.css` import; verify in network tab |
| Local media 403 (sandbox) | Medium | Medium | inline route reuses `resolveFilePath`; test in MD-006 |
| Bundle bloat from mermaid | Medium | Low | lazy `import('mermaid')`; verify chunk split |
| highlight.js theme unreadable in one mode | Low | Low | verify both themes against token palette |
