# Research: Rich Markdown Rendering in the Web UI

Status: Research complete. Feeds `spec.md`, `plan.md`, `tasks.md`.
Date: 2026-08-12

## Goal

Make `MarkdownRenderer.vue` render agent responses perfectly: GitHub-flavored
markdown, syntax-highlighted code, LaTeX equations (inline + display), Mermaid
diagrams, and embedded/linked media (images, video, audio). All under the
existing strict CSP and the DOMPurify sanitization gate.

## Current State (baseline)

- `webui/src/components/chat/MarkdownRenderer.vue` uses `markdown-it@15` with
  defaults (`html:false`, `linkify`, `typographer`, `breaks`) and `dompurify`
  with `ADD_ATTR:['target']`.
- `highlight.js@11` is already a dependency but is NOT wired into markdown-it.
- No math plugin, no diagram support, no media handling.
- `html:false` blocks any raw `<video>`/`<audio>`/`<iframe>` the model emits.
- Go backend (`internal/web/handlers/file.go`) only serves files as
  `Content-Disposition: attachment` via `GET /api/files/download`. There is no
  inline-serve route, so `<img src="...">` / `<video src="...">` cannot reference
  workspace files directly.
- Frontend is embedded into the Go binary via `//go:embed all:dist`
  (`internal/web/embed_prod.go`); a symlinked copy lives at `internal/web/dist`.

## CSP Analysis (the hard constraint)

`internal/web/middleware/headers.go` sets:

```
default-src 'self';
script-src 'self';                  # strict, no 'unsafe-inline', no 'unsafe-eval'
style-src 'self' 'unsafe-inline';   # inline styles OK (KaTeX + Mermaid need this)
img-src 'self' data: https:;        # external https images OK
font-src 'self';                    # KaTeX fonts must be bundled (Vite handles this)
connect-src 'self';
frame-ancestors 'none';
base-uri 'self';
form-action 'self'
```

Directive implications:

| Need | Directive | Status | Action |
| --- | --- | --- | --- |
| KaTeX JS (bundled) | `script-src 'self'` | OK | none |
| KaTeX CSS + inline styles | `style-src 'self' 'unsafe-inline'` | OK | none |
| KaTeX fonts (bundled by Vite) | `font-src 'self'` | OK | import `katex.min.css` so Vite emits fonts as `self` assets (small fonts may base64-inline; both satisfy CSP) |
| Mermaid JS (bundled) | `script-src 'self'` | OK | none |
| Mermaid SVG output (inline styles) | `style-src 'unsafe-inline'` | OK | none |
| Mermaid `eval`/`new Function` | `script-src` | **Not used** | Confirmed in mermaid source: strict/antiscript/sandbox paths never `eval`; no `unsafe-eval` needed |
| External images | `img-src ... https:` | OK | none |
| Local workspace media | `media-src` | **Missing** | falls back to `default-src 'self'`; add `media-src 'self' data:` (and optionally `https:`) |
| External/inline video & audio | `media-src` | **Missing** | add as above |
| External embeds (YouTube/Vimeo `<iframe>`) | `frame-src` | **Missing** | policy decision; add `frame-src https:` only if oembed iframes are desired |

**Conclusion:** Only `media-src` (and optionally `frame-src`) must be added.
`script-src 'self'` stays intact - no `unsafe-eval`, no `unsafe-inline`.

## Technology Decisions

### 1. Markdown core: keep `markdown-it@15`

Already in use, fast, pluggable. Build a single memoized instance via a
factory so streaming re-renders are cheap.

### 2. Syntax highlighting: `highlight.js` (already a dep)

Wire markdown-it's `highlight` option to `hljs.highlightAuto` / per-language.
Import one CSS theme that adapts to dark/light (or two themes swapped by the
existing theme store). No new dependency.

### 3. LaTeX math: `@vscode/markdown-it-katex` + `katex`

- Microsoft-maintained, used by VS Code itself, MIT, ~299k weekly downloads,
  latest release 1.1.2 (July 2025). Actively maintained.
- Beats `markdown-it-texmath` (last release v1.0 in May 2022, single author).
- Handles both `$...$` (inline) and `$$...$$` (display), plus fenced `math`
  blocks via `enableFencedBlocks`.
- Internally calls `katex.renderToString` with `throwOnError:false`-equivalent
  behavior - essential for malformed LaTeX from the model.
- KaTeX output uses inline `style` attributes and a MathML mirror
  (`.katex-mathml`). DOMPurify must keep both (see below).
- Import `katex/dist/katex.min.css` once in `main.ts` (or `globals.css`) so Vite
  bundles the fonts. This satisfies `font-src 'self'`.

### 4. Diagrams: `mermaid` v11

- Async API: `const { svg } = await mermaid.render(id, definition)`.
- markdown-it rendering is synchronous, so we emit a placeholder
  `<div class="mermaid-placeholder" data-mermaid-source="...">` in the fence
  renderer and hydrate it after `v-html` injects (Vue `onUpdated`/`nextTick`).
- `mermaid.initialize({ startOnLoop: false, securityLevel: 'strict',
  startOnLoad: false })`. `strict` (the default) encodes HTML in diagram text
  and runs its own DOMPurify over the SVG output - safe for AI content.
- Lazy-load mermaid (`() => import('mermaid')`) so it is never in the main
  chunk; diagrams are rare and the library is large (~1MB).
- Errors (partial source during streaming, bad syntax) must be caught: render a
  fallback `<pre>` with the source and an error badge instead of crashing.

### 5. Media: custom markdown-it media-link rule + inline endpoint

Markdown has no `<video>`/`<audio>` syntax and we keep `html:false`, so:

- Add a small markdown-it plugin (`mediaLinks`) that intercepts link tokens
  whose URL ends in a media extension OR is a known oembed host, and rewrites
  the token to the proper element:
  - `.png/.jpg/.jpeg/.gif/.webp/.svg/.avif` -> `<img>` (markdown-it already
    handles `![alt](url)`, but a bare `[file.png](url)` should also inline)
  - `.mp4/.webm/.mov/.m4v/.ogg` -> `<video controls>`
  - `.mp3/.wav/.flac/.m4a/.aac` -> `<audio controls>`
  - YouTube/Vimeo/Streamable watch URLs -> `<iframe>` (only when `frame-src`
    is relaxed; otherwise render as a styled link)
- Workspace-relative URLs (no scheme, no leading `/`) are rewritten to
  `/api/files/inline?path=<enc>` so local artifacts (`outputs/chart.mp4`,
  `.hakase-tmp/attachments/x.png`) display inline. Absolute `http(s)://` and
  `data:` URLs pass through (CSP permitting).
- Add `GET /api/files/inline?path=` to the Go backend: same sandbox resolution
  as `download`, but `Content-Disposition: inline` and a strict
  `Content-Type`. Gated by the existing auth + sandbox.

### 6. Sanitization: `dompurify` configuration

KaTeX output and MathML must survive sanitization. New config:

```js
DOMPurify.sanitize(html, {
  USE_PROFILES: { html: true, mathMl: true }, // keep KaTeX MathML mirror
  ADD_ATTR: ['target', 'rel', 'class', 'style', 'data-mermaid-source'],
  ALLOW_DATA_ATTR: true,
})
```

- `style` is added because KaTeX emits inline `style` attributes; this is
  acceptable because `style-src 'unsafe-inline'` is already in the CSP and the
  input is model output behind auth. (A tighter `uponSanitizeAttribute` hook
  that only keeps `style` inside `.katex` is documented as a hardening option.)
- Mermaid SVG is injected AFTER sanitization (post-mount hydration); mermaid's
  own `strict` securityLevel already DOMPurify-sanitizes its SVG, so it is
  pre-cleaned. The placeholder `data-mermaid-source` attribute must be on the
  allow-list so it survives sanitization until hydration.

### 7. Link hardening

Register a `link_open` rule that adds `target="_blank"` +
`rel="noopener noreferrer"` to absolute external links. Relative links keep
default behavior.

## Streaming Considerations

- Re-render on every delta is the existing pattern (computed over `content`).
- Math with `throwOnError:false` renders partial `$$...` gracefully (red
  source text) - acceptable; no debounce needed.
- Mermaid render is debounced per-block: a block is only rendered once its
  source has been stable for one tick OR the message is marked complete, and
  each placeholder is rendered at most once (tracked by a render cache keyed on
  the source hash). Failed blocks show the fallback `<pre>` and are retried
  only when their source changes.
- highlight.js is synchronous and fast; no special streaming handling.

## Build / Packaging

- `pnpm add katex @vscode/markdown-it-katex mermaid` in `webui/`.
- `make build-frontend` mirrors `webui/dist` into `internal/web/dist` for
  `//go:embed`. No Go changes required for the frontend deps.
- Lazy `import('mermaid')` keeps it out of the initial bundle.

## Rejected Alternatives

- `markdown-it-texmath`: stale (2022), single maintainer.
- `mathjax`: much larger, slower async rendering, more CSP friction.
- Server-side KaTeX (Go): no good Go KaTeX port; would fragment the pipeline.
- `marked` + custom KaTeX extension: would require replacing markdown-it.
- `unsafe-eval` in CSP: not needed (confirmed) and would weaken the policy.
- Allow `html:true` in markdown-it: rejected; the custom media-link rule and
  KaTeX/Mermaid plugins cover the needs without raw-HTML injection.

## Open Policy Questions (need a decision before tasks 5-6)

1. **External media** (`media-src https:`): allow agent responses to embed
   arbitrary https video/audio? Default proposal: allow `media-src 'self' data:`
   only (no external); external media renders as a styled link. Relax to
   `https:` if external playback is desired.
2. **External embeds** (`frame-src https:`): allow YouTube/Vimeo iframes?
   Default proposal: leave blocked (render as a styled card link). Relax only if
   embedded playback is desired.

### RESOLVED (2026-08-12)

- **Q1 - External media: DENIED for v1.** Adopt the default `media-src 'self'
  data:`. No `https:` in `media-src`. External video/audio URLs render as
  styled links (the markdown-it default link token). Rationale: keeps the CSP
  minimal, avoids leaking viewer IPs to arbitrary media hosts, and the model can
  re-emit local copies via the sandbox when inline playback matters.
- **Q2 - External embeds: DENIED for v1.** No `frame-src https:`. No
  `<iframe>` emission; the `ENABLE_OEMBED` branch in the mediaLinks plugin stays
  compiled-out behind its flag. Rationale: frames are the largest CSP/blast-
  radius increase and blocking them preserves `frame-ancestors 'none'`
  semantics for our own page.
- **Q3 - HTTP Range for `GET /api/files/inline`: NOT in v1.** The route streams
  the whole file with `Content-Type` + `Content-Disposition: inline`. Browsers
  still play whole-file media; seeking requires Range, noted as a v2 follow-up
  if the model commonly emits long local videos.

These decisions feed tasks T0.1/T0.2 and lock MD-007 to `media-src 'self' data:`
(default path) and MD-006 to whole-file streaming.
