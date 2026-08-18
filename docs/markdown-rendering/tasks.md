# Task List: Rich Markdown Rendering in the Web UI

Feature: `markdown-rendering`
Atomic, hand-offable tasks. Each references the governing spec in `spec.md`
and is sized to 1-3 tool calls + one verification step.

Legend: `[FE]` frontend, `[BE]` Go backend, `[QA]` test/docs. Status: TODO.

---

## Phase 0 - Decisions

- [x] **T0.1** Decide CSP media policy (default vs `https:` media vs `frame-src`).
      Write the chosen option into `research.md` "Open Policy Questions".
      Governs: MD-007. _Owner: decision-maker._
- [x] **T0.2** Decide whether MD-006 v1 needs HTTP Range support.
      Governs: MD-006.

---

## Phase 1 - Foundation (MD-001)

- [x] **T1.1 `[FE]`** `cd webui && pnpm add katex @vscode/markdown-it-katex`.
      Verify: `pnpm install` clean; versions pinned in `package.json`.
      Spec: MD-001.
- [x] **T1.2 `[FE]`** Create `webui/src/lib/markdown/index.ts` exporting
      `renderMarkdown(src): string` and `md` (the MarkdownIt instance). Configure
      markdown-it (`html:false`, `linkify`, `typographer`, `breaks`), register
      `@vscode/markdown-it-katex`, and sanitize with DOMPurify
      (`USE_PROFILES:{html:true,mathMl:true}`,
      `ADD_ATTR:['target','rel','class','style','data-mermaid-source']`,
      `ALLOW_DATA_ATTR:true`). Share one KaTeX `macros` object across calls.
      Verify: `console.log(renderMarkdown('$a^2$'))` shows a `.katex` span.
      Spec: MD-001.
- [x] **T1.3 `[FE]`** Import `katex/dist/katex.min.css` in `main.ts` (or
      `globals.css`). Verify: `pnpm build` emits KaTeX fonts under
      `dist/assets/` (or inlines small ones); no external font requests at
      runtime. Spec: MD-001.

---

## Phase 2 - Parallel tracks

### Track A - Syntax highlighting (MD-002)

- [x] **T2A.1 `[FE]`** In `webui/src/lib/markdown/highlight.ts`, export
      `highlight(code, lang): string` using `highlight.js` (per-language when
      known, else `highlightAuto`, never throw). Wire it into the markdown-it
      `highlight` option in `index.ts`. Verify: ` ```python\nprint(1)\n``` `
      yields `hljs`-classed spans. Spec: MD-002.
- [x] **T2A.2 `[FE]`** Pick a dark and a light highlight.js theme
      (e.g. `github-dark.css` + `github.css`). Import both lazily; in
      `webui/src/stores/theme.ts`, toggle the active theme stylesheet based on
      `isDark`. Verify: code colors switch when toggling theme. Spec: MD-002.

### Track B - Media links (MD-003)

- [x] **T2B.1 `[FE]`** Create `webui/src/lib/markdown/plugins/mediaLinks.ts`
      exporting `mediaLinks(): PluginSimple`. Implement a `link_open`/`image`
      rule that detects media by extension and rewrites the token to
      `<video controls>`/`<audio controls>`/`<img>`; rewrites workspace-relative
      URLs (no scheme, no leading `/`) to `/api/files/inline?path=<enc>`.
      Register in `index.ts`. Verify: `[x](./a.mp4)` -> `<video controls src="/api/files/inline?path=a.mp4">`.
      Spec: MD-003.
- [x] **T2B.2 `[FE]`** (Optional, only if T0.1 enables frames) Add an oembed
      branch for YouTube/Vimeo/Streamable watch URLs emitting `<iframe>` with
      `allow`/`referrerpolicy`. Guard behind an exported `ENABLE_OEMBED` flag.
      *(skipped: oembed not enabled per T0.1; no `frame-src` in CSP.)*
      Spec: MD-003/MD-007.

### Track C - Mermaid (MD-004)

- [x] **T2C.1 `[FE]`** `cd webui && pnpm add mermaid`. Verify clean install.
      Spec: MD-004.
- [x] **T2C.2 `[FE]`** Create `webui/src/lib/markdown/plugins/mermaidPlaceholder.ts`
      as a markdown-it fence renderer that emits
      `<div class="mermaid-placeholder" data-mermaid-source="<escaped>"></div>`
      for the `mermaid` language. Register in `index.ts`. Verify: a mermaid
      fence produces a placeholder div, not a code block. Spec: MD-004.
- [x] **T2C.3 `[FE]`** Create `webui/src/composables/useMermaid.ts` exporting
      `useMermaid()` -> `{ hydrate(root), dispose() }`. Lazy
      `import('mermaid')`; `initialize({ startOnLoad:false, securityLevel:'strict' })`;
      on `hydrate`, find unrendered placeholders, call `mermaid.render(id, src)`,
      swap `innerHTML`, and on error render a fallback `<pre class="mermaid-error">`.
      Keep a module-level render cache keyed on source hash so each source is
      rendered once. Verify: call `hydrate` on a div with a known-good source;
      SVG appears. Spec: MD-004.

### Track D - Inline file route (MD-006)

- [x] **T2D.1 `[BE]`** In `internal/web/handlers/file.go`, add
      `InlineFile(w,r)` modeled on `DownloadFile` but with
      `Content-Disposition: inline`, MIME from extension, and (if T0.2 enables)
      `http.ServeContent` for Range support. Register `GET /api/files/inline`
      in `RegisterFileRoutes`. Verify: `go build ./...` and a manual
      `curl -u <jwt> http://localhost:8080/api/files/inline?path=outputs/x.png`.
      Spec: MD-006.
- [x] **T2D.2 `[BE]`** Add table-driven tests in `file_test.go`: 200 inline,
      403 outside sandbox, 404 missing, 400 directory, 401 without token.
      Verify: `go test ./internal/web/...`. Spec: MD-006/MD-008.

### Track E - CSP (MD-007)

- [x] **T2E.1 `[BE]`** In `internal/web/middleware/headers.go`, add
      `media-src` per T0.1 (default: `'self' data:`). Add `frame-src https:`
      only if T0.1 enables oembed. Do NOT change `script-src`. Verify: response
      headers show the new directive; CSP report endpoint clean. Spec: MD-007.
- [x] **T2E.2 `[BE]`** Update `headers_test.go` to assert `media-src` presence
      and that `script-src` is still exactly `'self'`. Verify:
      `go test ./internal/web/middleware/...`. Spec: MD-007/MD-008.

---

## Phase 3 - Integration (MD-005)

- [x] **T3.1 `[FE]`** Rewrite `webui/src/components/chat/MarkdownRenderer.vue`
      to use `renderMarkdown(content)`; on mount and `onUpdated`/`nextTick`,
      call `useMermaid().hydrate(rootEl)`; clean up on unmount. Pass
      `streaming` from `MessageBubble` through. Verify: render a rich payload
      fixture; no console errors during simulated streaming. Spec: MD-005.
- [x] **T3.2 `[FE]`** Add a copy-to-clipboard button to `<pre>` code blocks
      (delegated handler on the root, target `pre > code`). Verify: clicking
      copies the raw code. Spec: MD-005.
- [x] **T3.3 `[FE]`** Add a `link_open` rule (in `index.ts`) that sets
      `target="_blank" rel="noopener noreferrer"` on absolute external links.
      Verify: external links open in a new tab. Spec: MD-005.

---

## Phase 4 - Hardening (MD-008)

- [x] **T4.1 `[FE]`** Create `webui/src/lib/markdown/markdown.test.ts`
      covering: headings/code/tables/lists; inline `$...$`; display `$$...$$`;
      fenced `math`; mermaid placeholder; media rewrites
      (img/video/audio/local/external/data); sanitization (`<script>`,
      `onerror=`, `<iframe>` when frames disabled; `style` kept inside `.katex`).
      Add a `test` script to `webui/package.json` if missing (e.g. `vitest`).
      Verify: `pnpm test`. Spec: MD-008.
- [x] **T4.2 `[QA]`** Write `docs/markdown-rendering/fixtures.md` with
      before/after for each payload class; sign off the QA matrix:
      headings, fenced code + copy, table, inline math, display math, fenced
      math, mermaid (valid + invalid), image (external + local), video (local),
      audio (local), sanitization probes. Verify: all green in a running
      `hakase web`. Spec: MD-008.

---

## Verification Commands

```bash
# Frontend
cd webui
pnpm install
pnpm build            # check chunk split: mermaid in its own chunk; KaTeX fonts emitted
pnpm test             # markdown.test.ts (add vitest if not present)
pnpm dev              # manual QA against `make dev-backend`

# Backend
go test ./internal/web/...
go test ./internal/web/middleware/...
make build            # full prod binary with embedded SPA

# End-to-end
make dev-backend      # :8080
# open the dev URL, send a prompt that yields math + mermaid + image + video
```

## QA Matrix (for fixtures.md sign-off)

| Payload | Expected |
| --- | --- |
| `# H1\n**bold** *italic*` | heading, bold, italic |
| ` ```python\nprint('hi')\n``` ` | highlighted code + copy button |
| `\| a \| b \|` table | rendered table |
| `E = mc^2` inline | KaTeX inline |
| `$$\int_0^1 x\,dx$$` | KaTeX display |
| ` ```math\nx^2\n``` ` | KaTeX display (fenced) |
| ` ```mermaid\ngraph LR\nA-->B\n``` ` | mermaid SVG |
| malformed ` ```mermaid\n!!!broken ` | fallback `<pre>` error block |
| `![alt](https://.../img.png)` | external `<img>` |
| `![alt](./outputs/a.png)` | local `<img>` via `/api/files/inline` |
| `[clip](./outputs/v.mp4)` | `<video controls>` |
| `[song](./outputs/a.mp3)` | `<audio controls>` |
| `<script>alert(1)</script>` | stripped |
| `<img src=x onerror=alert(1)>` | `onerror` stripped |
