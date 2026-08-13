// Syntax highlighting for markdown-it fenced code blocks.
// Uses highlight.js (already a project dependency). Known languages are
// highlighted explicitly; unknown or absent languages fall back to
// highlightAuto. Never throws - an unhighlightable block degrades to plain
// escaped text.

import hljs from 'highlight.js'

function htmlEscape(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/**
 * highlight returns the highlighted token HTML (without the wrapping
 * <pre>/<code>) for the given code and optional language hint.
 */
export function highlight(code: string, lang?: string): string {
  try {
    if (lang && hljs.getLanguage(lang)) {
      return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value
    }
    return hljs.highlightAuto(code).value
  } catch {
    return htmlEscape(code)
  }
}

/**
 * highlightCodeBlock renders a full <pre><code class="hljs"> block, ready to
 * be returned straight from markdown-it's `highlight` option (markdown-it
 * returns it verbatim because it starts with `<pre`).
 */
export function highlightCodeBlock(code: string, lang?: string | null): string {
  const body = highlight(code, lang ?? undefined)
  const langClass =
    lang && hljs.getLanguage(lang) ? ` language-${htmlEscape(lang)}` : ''
  return `<pre class="hljs"><code class="hljs${langClass}">${body}</code></pre>`
}