// Markdown rendering pipeline for the web UI.
//
// One memoized markdown-it instance powers all chat rendering:
//
//   - html:false (no raw-HTML injection from model output)
//   - KaTeX via @vscode/markdown-it-katex (inline $...$, display $$...$$,
//     fenced ```math) with a single shared `macros` object so \gdef persists
//   - highlight.js syntax highlighting (fenced ```lang blocks)
//   - a mermaid fence renderer that emits hydratable placeholders
//   - a media-link rule that rewrites audio/video/image links (and
//     workspace-relative paths to /api/files/inline)
//   - link hardening: absolute external links open in a new tab
//
// Every model-derived string passes through DOMPurify before hitting v-html.
// The KaTeX MathML mirror and inline `style` attributes are preserved so math
// stays accessible and styled under the existing CSP.

import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'
import type { Config as DOMPurifyConfig } from 'dompurify'
import markdownItKatexModule from '@vscode/markdown-it-katex'
import { highlightCodeBlock } from './highlight'

// @vscode/markdown-it-katex is plain CJS (main: dist/index.js, no `exports`
// map, __esModule + `default`). Under rolldown's node-interop the default
// import can resolve to the whole module object `{ default: fn }` instead of
// the plugin function itself (see isNodeMode in the runtime __toESM helper).
// Unwrap it so md.use() always receives a callable in both dev and prod.
type KatexPlugin = (md: MarkdownIt, options?: Record<string, unknown>) => void
const markdownItKatex: KatexPlugin = (
  (markdownItKatexModule as { default?: KatexPlugin }).default ??
  (markdownItKatexModule as unknown as KatexPlugin)
)
import { mediaLinks } from './plugins/mediaLinks'
import { mermaidPlaceholder } from './plugins/mermaidPlaceholder'

// Shared KaTeX macros object: keeps \gdef definitions usable across every
// equation in a message (and session). Reset per render if cross-message
// leakage is ever observed.
export const sharedMacros: Record<string, string> = {}

const SANITIZE_OPTIONS: DOMPurifyConfig = {
  USE_PROFILES: { html: true, mathMl: true },
  ADD_ATTR: ['target', 'rel', 'class', 'style', 'data-mermaid-source'],
  ALLOW_DATA_ATTR: true,
}

function buildMarkdownIt(): MarkdownIt {
  const instance = new MarkdownIt({
    html: false,
    linkify: true,
    typographer: true,
    breaks: true,
    highlight: (code: string, lang: string) => highlightCodeBlock(code, lang),
  })

  // KaTeX last so its fence override (```math) wraps markdown-it's built-in
  // fence (which runs our highlight option for every other language).
  instance.use(markdownItKatex, {
    enableFencedBlocks: true,
    throwOnError: false,
    macros: sharedMacros,
  })

  // Mermaid last: its fence override wraps the KaTeX-wrapped fence and only
  // intercepts ```mermaid blocks.
  instance.use(mermaidPlaceholder())

  // Media-link rewriting (runs in the core ruler, before rendering).
  instance.use(mediaLinks())

  // External-link hardening: absolute http(s) links open in a new tab.
  instance.renderer.rules.link_open = (
    tokens: MarkdownIt.Token[],
    idx: number,
    options: MarkdownIt.Options,
    _env: unknown,
    self: MarkdownIt.Renderer,
  ): string => {
    const token = tokens[idx]
    const href = token.attrGet('href') ?? ''
    if (/^https?:\/\//i.test(href)) {
      token.attrSet('target', '_blank')
      token.attrSet('rel', 'noopener noreferrer')
    }
    return self.renderToken(tokens, idx, options)
  }

  return instance
}

// The single memoized instance shared by every render call.
export const md = buildMarkdownIt()

/**
 * renderMarkdown renders markdown source to sanitized HTML suitable for
 * v-html. Safe for streaming: called on every content delta.
 * 
 * Resets KaTeX macro state for each render to prevent \gdef definitions
 * from leaking between messages.
 */
export function renderMarkdown(source: string): string {
  if (!source) return ''
  
  // Clear KaTeX macros before each render to prevent cross-message leakage
  // Note: This affects the sharedMacros object used by the KaTeX plugin
  Object.keys(sharedMacros).forEach(key => delete sharedMacros[key])
  
  const raw = md.render(source)
  return DOMPurify.sanitize(raw, SANITIZE_OPTIONS) as string
}