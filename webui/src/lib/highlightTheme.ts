// highlight.js theme management.
//
// Both themes are bundled (as `?inline` CSS strings) so nothing loads from a
// CDN, satisfying CSP `style-src`. The active theme is injected into a single
// <style data-hljs-theme> element; switching light/dark swaps its textContent.
// CSP `style-src 'unsafe-inline'` permits the injected style element.

import lightTheme from 'highlight.js/styles/github.css?inline'
import darkTheme from 'highlight.js/styles/github-dark.css?inline'
import type { Theme } from '@/stores/theme'

const DATA_ATTR = 'data-hljs-theme'

function ensureStyleEl(): HTMLStyleElement {
  let el = document.head.querySelector<HTMLStyleElement>(`style[${DATA_ATTR}]`)
  if (!el) {
    el = document.createElement('style')
    el.setAttribute(DATA_ATTR, '')
    document.head.appendChild(el)
  }
  return el
}

export function applyHighlightTheme(theme: Theme): void {
  ensureStyleEl().textContent = theme === 'dark' ? darkTheme : lightTheme
}