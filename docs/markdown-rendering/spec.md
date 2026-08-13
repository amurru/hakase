# Development Blueprint: Rich Markdown Rendering in the Web UI

Feature: `markdown-rendering`
Status: Spec ready for execution. See `plan.md` (sequence) and `tasks.md` (atomic tasks).
Scope: frontend (`webui/`) + one Go route + one CSP delta.

## Context and Objective

The web UI's `MarkdownRenderer.vue` currently renders plain markdown via a
default `markdown-it` instance. It does not syntax-highlight (despite
`highlight.js` being a dependency), does not render LaTeX, does not render
Mermaid diagrams, and - because `html:false` - cannot display any media the
agent emits beyond `![alt](url)` images.

Objective: render agent markdown responses perfectly, with:

1. GitHub-flavored markdown + accurate syntax highlighting.
2. LaTeX equations: inline `$...$` and display `$$...$$` (and fenced `math`).
3. Mermaid charts/diagrams (` ```mermaid ` fenced blocks).
4. Embedded or externally linked media: images, video, audio (and optional
   oembed iframes), covering both absolute `http(s)`/`data:` URLs and
   workspace-relative file paths.

All rendering must stay safe under the existing strict CSP and the DOMPurify
sanitization gate, and must remain robust while the response streams.

## Repository Dependency Map

| Path | Role | Dependencies / Touchpoints | Risk |
| --- | --- | --- | --- |
| `webui/src/components/chat/MarkdownRenderer.vue` | The renderer component; only consumer of the pipeline. | Rewritten to use the new pipeline + post-mount mermaid hydration. | High (core change) |
| `webui/src/components/chat/MessageBubble.vue` | Mounts `MarkdownRenderer`; passes streaming flag. | Pass `streaming` prop through; no logic change needed. | Low |
| `webui/src/components/chat/ThinkingBlock.vue` | Shows raw thinking text. | Optional: render limited markdown (no mermaid). Out of scope for v1. | Low |
| `webui/src/lib/markdown.ts` (new) | Pipeline factory + plugins + DOMPurify config. | Imports markdown-it, highlight.js, KaTeX plugin, mermaid (lazy). | High (new) |
| `webui/src/composables/useMermaid.ts` (new) | Async mermaid hydration + render cache + error fallback. | Lazy-imports mermaid; mutates placeholder DOM. | Medium (new) |
| `webui/src/main.ts` / `webui/src/assets/globals.css` | Import `katex.min.css` + highlight.js theme(s). | Build-time font/theme bundling. | Low |
| `webui/package.json` | Add `katex`, `@vscode/markdown-it-katex`, `mermaid`. | pnpm lockfile. | Low |
| `internal/web/middleware/headers.go` | CSP header. | Add `media-src` (and optional `frame-src`). | Medium (security policy) |
| `internal/web/handlers/file.go` | File routes. | Add `GET /api/files/inline`. | Medium |
| `internal/web/spa.go` | Route registration. | Register the inline route inside the auth group. | Low |
| `internal/web/handlers/file_test.go` | Existing file-route tests. | Add coverage for inline route + CSP. | Low |
| `internal/web/middleware/headers_test.go` | CSP test. | Assert new directives. | Low |

No other modules are affected. Agent output format is unchanged.

## Architectural Guardrails

### Allowed

- `markdown-it` plugin/rule customization; a single memoized instance via a
  factory in `webui/src/lib/markdown.ts`.
- Bundling KaTeX CSS (`katex/dist/katex.min.css`) so Vite emits fonts as
  same-origin assets (keeps `font-src 'self'` valid).
- Lazy `import('mermaid')` so the main bundle stays small.
- A custom markdown-it rule that rewrites media links to `<video>`/`<audio>`/
  `<img>` tokens (keeps `html:false`).
- Post-mount DOM hydration for mermaid placeholders (Vue `onUpdated` +
  `nextTick`, scoped to the component root).
- DOMPurify with `USE_PROFILES: { html: true, mathMl: true }` and the
  `data-mermaid-source` attribute on the allow-list.
- A new authenticated, sandbox-resolved `GET /api/files/inline` route that
  serves workspace files with `Content-Disposition: inline`.

### Forbidden

- Setting markdown-it `html:true` (raw-HTML injection from model output).
- Adding `'unsafe-eval'` or `'unsafe-inline'` to `script-src`. Mermaid needs
  neither (verified in mermaid source).
- Loading KaTeX/Mermaid/highlight.js fonts or scripts from a CDN (must be
  bundled = `'self'`).
- Injecting mermaid SVG without relying on mermaid's `strict` securityLevel
  (which DOMPurify-sanitizes its own output).
- Bypassing the sandbox for the inline file route; it must reuse
  `resolveFilePath` / `sandbox.CurrentSandbox`.
- Skipping DOMPurify for any model-derived HTML.

### Constraints

- CSP `script-src 'self'` is inviolable. All JS is bundled.
- CSP `style-src 'unsafe-inline'` is already present - KaTeX and Mermaid rely
  on it; do not remove.
- `font-src 'self'` - KaTeX fonts must be bundled, not CDN-loaded.
- Streaming-safe: the pipeline is invoked on every content delta. Math must
  degrade gracefully on partial input; mermaid must never throw synchronously.
- Performance: re-rendering on every token must stay cheap - memoize the
  markdown-it instance, avoid re-running mermaid on stable blocks.
- Threat model: input is authenticated model output; sanitization + CSP are
  the two layers. Both must hold for every code path.
- Accessibility: KaTeX MathML mirror must not be stripped (screen readers).
- Backward compatibility: existing markdown (no math/mermaid/media) must render
  byte-for-byte the same as today (apart from gaining syntax highlighting).

## Atomic Specs

### Spec MD-001: Pipeline factory and DOMPurify config
- Objective: Create `webui/src/lib/markdown.ts` exposing `renderMarkdown(src):
  string` backed by one memoized `markdown-it` instance with the KaTeX plugin
  registered, a safe DOMPurify config, and `html:false`. This is the foundation
  every other spec builds on.
- Acceptance Criteria:
  - `renderMarkdown('# h\n**b**')` returns sanitized HTML matching today's
    output shape (headings, bold, lists).
  - `renderMarkdown('$a^2$')` produces a `.katex` span (math rendered).
  - `renderMarkdown('<script>alert(1)</script>')` returns empty / no script tag.
  - `renderMarkdown('<img src=x onerror=alert(1)>')` has no `onerror` attr.
  - MathML tags (`<math>`, `<mrow>`, `<mi>`, ...) survive DOMPurify.
  - `style` attributes inside KaTeX output survive sanitization.
- Affected Components: `webui/src/lib/markdown.ts` (new); `webui/package.json`
  (add `katex`, `@vscode/markdown-it-katex`).
- Contracts / Interfaces:
  ```ts
  export function renderMarkdown(source: string): string  // sanitized HTML
  export const md: MarkdownIt                              // raw instance (for advanced use)
  ```
- Guardrails: `html:false`; single shared `macros` object across calls (KaTeX
  `\gdef` persistence); no CDN imports.
- Dependencies: none.

### Spec MD-002: Syntax highlighting via highlight.js
- Objective: Wire markdown-it's `highlight` option to `highlight.js` so fenced
  code blocks get language-aware highlighting with a theme that adapts to
  dark/light.
- Acceptance Criteria:
  - A ` ```python\nprint(1)\n``` ` block renders `<pre><code>` with
    `hljs`-classed spans.
  - Unknown language falls back to `highlightAuto` or plain text without
    throwing.
  - One CSS theme is imported; the active theme is swapped by the existing
    theme store (`useThemeStore`) so light/dark both look correct.
  - Highlighting does not break inline `code`.
- Affected Components: `webui/src/lib/markdown.ts`; `webui/src/assets/globals.css`
  or `main.ts` (theme import); `webui/src/stores/theme.ts` (theme swap hook).
- Contracts: `highlight(str, lang): string` helper used by markdown-it config.
- Guardrails: synchronous; no network; bundled theme only.
- Dependencies: MD-001.

### Spec MD-003: Media-link rendering rule
- Objective: Add a markdown-it plugin that rewrites media links (and bare image
  links) to `<img>`/`<video controls>`/`<audio controls>` tokens, and rewrites
  workspace-relative URLs to `/api/files/inline?path=...`.
- Acceptance Criteria:
  - `[clip](https://example.com/v.mp4)` -> `<video controls src="...">`.
  - `[song](./outputs/a.mp3)` -> `<audio controls src="/api/files/inline?path=outputs%2Fa.mp3">`.
  - `![pic](./outputs/a.png)` and `[pic](./outputs/a.png)` both -> `<img>`.
  - External `https://` image stays absolute (CSP `img-src https:` allows it).
  - Unknown extensions render as normal links (no behavior change).
  - `data:` URLs pass through for images (CSP `img-src data:` allows it).
  - No new inline event handlers or scripts are emitted.
- Affected Components: `webui/src/lib/markdown/plugins/mediaLinks.ts` (new);
  wired in `webui/src/lib/markdown.ts`.
- Contracts: plugin factory `mediaLinks(): markdown-it.PluginSimple`.
- Guardrails: extension allowlists only; `controls` always set on media; never
  emit `<iframe>` unless a separate `frame-src` spec is enabled (see MD-007).
- Dependencies: MD-001. (MD-006 unblocks local-file playback.)

### Spec MD-004: Mermaid placeholder + post-mount hydration
- Objective: Render ` ```mermaid ` fenced blocks as placeholders, then hydrate
  them into SVG via the async `mermaid.render` API after Vue mounts/updates.
- Acceptance Criteria:
  - ` ```mermaid\ngraph LR\nA-->B\n``` ` shows the diagram SVG.
  - Malformed source renders a fallback `<pre class="mermaid-error">` with the
    raw source and an error badge; the component does not throw.
  - During streaming, a given source string is rendered at most once (render
    cache keyed on source hash); a changed source re-renders that block only.
  - Mermaid is loaded lazily (`import('mermaid')`); it is absent from the main
    JS chunk (verified via `pnpm build` output sizes / manual chunk check).
  - `securityLevel: 'strict'` is set; clicking interactions are disabled.
  - The placeholder survives DOMPurify via the allow-listed
    `data-mermaid-source` attribute.
- Affected Components: `webui/src/lib/markdown/plugins/mermaidPlaceholder.ts`
  (new); `webui/src/composables/useMermaid.ts` (new);
  `webui/src/components/chat/MarkdownRenderer.vue` (hydrate on
  `onUpdated`/`nextTick`, scoped to root).
- Contracts:
  ```ts
  // composable
  export function useMermaid(): {
    hydrate(root: HTMLElement): void   // idempotent per placeholder
    dispose(): void
  }
  ```
- Guardrails: never `eval`/`new Function` (mermaid does not need it); never
  inject mermaid SVG that skipped mermaid's own `strict` sanitization; scope
  DOM queries to the component root to avoid cross-message interference.
- Dependencies: MD-001.

### Spec MD-005: Wire the new pipeline into MarkdownRenderer.vue
- Objective: Replace the inline markdown-it setup in `MarkdownRenderer.vue`
  with `renderMarkdown()` and mount mermaid hydration + a copy-to-clipboard
  affordance on code blocks.
- Acceptance Criteria:
  - `MessageBubble.vue` renders identical content for plain markdown (regression
    check) and correct math/mermaid/media for rich content.
  - When `streaming` is true, partial math degrades gracefully and mermaid
    blocks hydrate as they complete; no console errors.
  - Code blocks show a copy button on hover that copies the raw code.
  - External links open in a new tab with `rel="noopener noreferrer"`.
  - No layout regressions in the chat bubble (tables, blockquotes, lists).
- Affected Components: `webui/src/components/chat/MarkdownRenderer.vue`;
  `webui/src/components/chat/MessageBubble.vue` (pass-through only, if needed).
- Contracts: `<MarkdownRenderer :content="..." :streaming="..." />`.
- Guardrails: hydration scoped to the component root; cleanup on unmount.
- Dependencies: MD-001, MD-002, MD-003, MD-004.

### Spec MD-006: Inline file-serving endpoint
- Objective: Add `GET /api/files/inline?path=<p>` to serve a workspace file with
  `Content-Disposition: inline` and a correct `Content-Type`, for `<img>`/
  `<video>`/`<audio>` sources.
- Acceptance Criteria:
  - Reuses `resolveFilePath` (sandbox) - rejects paths outside the workspace
    with 403, mirroring `DownloadFile`.
  - Sets `Content-Type` from extension; sets `Content-Disposition: inline`.
  - Auth-gated (registered inside the `/api` auth group).
  - 404 for missing files; 400 for directories.
  - Range support is optional but recommended for video (`Accept-Ranges`,
    `Range`/`206`); v1 may stream the whole file.
- Affected Components: `internal/web/handlers/file.go` (new handler +
  route in `RegisterFileRoutes`); `internal/web/handlers/file_test.go` (tests).
- Contracts: route `GET /api/files/inline?path=<enc>`; same error envelope as
  the other file routes (`{"error": "..."}`).
- Guardrails: never serve outside sandbox roots; never disable auth; inline
  disposition only (no `attachment`); set `X-Content-Type-Options: nosniff`
  (already global via middleware).
- Dependencies: none on frontend specs; pairs with MD-003 for end-to-end local
  media.

### Spec MD-007: CSP update for media (and optional frames)
- Objective: Update the Content-Security-Policy in
  `internal/web/middleware/headers.go` to permit media playback, and optionally
  framed oembeds.
- Acceptance Criteria:
  - `media-src` added with at least `'self' data:`.
  - If external playback is approved: add `https:` to `media-src`.
  - If oembed iframes are approved: add `frame-src https:` (and the mediaLinks
    plugin gains an oembed branch emitting `<iframe>`).
  - Existing directives unchanged; `script-src 'self'` remains intact.
  - `headers_test.go` asserts the new directive(s) and that `script-src` is
    unchanged.
- Affected Components: `internal/web/middleware/headers.go`;
  `internal/web/middleware/headers_test.go`.
- Contracts: the CSP string gains the chosen `media-src` (and optional
  `frame-src`) clauses.
- Guardrails: do NOT relax `script-src`, `connect-src`, `default-src`, or
  `frame-ancestors`. Document the chosen policy in `research.md`.
- Dependencies: needs the policy decision in Open Questions (default: `'self'
  data:` only, no frames).

### Spec MD-008: Tests and regression fixtures
- Objective: Lock behavior with unit tests for the pipeline and a Go test for
  the inline route + CSP, plus a fixtures file of representative payloads.
- Acceptance Criteria:
  - `webui/src/lib/markdown/markdown.test.ts` covers: headings/code/tables,
    `$...$`, `$$...$$`, fenced `math`, ` ```mermaid ` placeholder emission,
    media-link rewrites (img/video/audio/local/external/data), sanitization
    (`<script>`, `onerror`, `<iframe>` when frames disabled).
  - `file_test.go` covers inline route: 200 with inline disposition, 403 outside
    sandbox, 404 missing, auth required.
  - `headers_test.go` covers the new CSP.
  - A `docs/markdown-rendering/fixtures.md` captures before/after examples.
- Affected Components: test files + fixtures doc.
- Guardrails: tests must run under `pnpm test` and `go test ./...`.
- Dependencies: MD-001..MD-007.

## Execution Sequence

1. **MD-001** (blocks 002, 003, 004, 005) - foundation.
2. **MD-006** and **MD-007** in parallel (backend; no frontend dependency).
3. **MD-002**, **MD-003**, **MD-004** in parallel (all depend only on 001).
4. **MD-005** (depends on 002, 003, 004) - integrate.
5. **MD-008** (depends on all) - lock it down.

Parallelization: MD-002/003/004 are independent frontend tracks; MD-006/007 are
independent backend tracks. Five work streams can proceed concurrently after
MD-001.

## Open Questions and Risks

1. **Media/iframe CSP policy** (blocks MD-007 finalization, not the default
   path): decide external `media-src https:` and/or `frame-src https:`. Default
   proposed in `research.md`: local + data only; external as links.
2. **Video range requests**: needed for seeking in long videos. v1 may stream
   whole file; confirm whether the model commonly emits large local videos.
3. **Mermaid bundle size**: ~1MB even when lazy-loaded. Acceptable given it is
    code-split and rare. Monitor.
4. **KaTeX macro persistence**: a single shared `macros` object enables `\gdef`
   across blocks but could leak between messages. Mitigation: scope macros per
   `MarkdownRenderer` instance (reset on message change) if cross-message
   leakage is observed.
5. **Theme contrast**: highlight.js theme must be readable in both light/dark;
   verify against the existing token palette in `globals.css`.

## Quality Control Checklist

- [x] Each atomic spec is independently testable (see MD-008).
- [x] Dependency map reflects real files (verified by read).
- [x] Guardrails are specific (CSP directives named; no vague platitudes).
- [x] Sequence respects dependencies.
- [x] An engineer unfamiliar with the project could execute any spec standalone.
