// Mermaid hydration composable.
//
// The markdown pipeline emits `div.mermaid-placeholder[data-mermaid-source]`
// for ```mermaid fences; this composable hydrates them into SVGs after the
// holding component mounts/updates. mermaid is lazy-imported so it never lands
// in the main JS chunk, and every render is cached keyed on the source hash so
// the same diagram is rendered at most once per session.
//
// securityLevel: 'strict' makes mermaid sanitize its own SVG output (its
// internal DOMPurify pass), so hydrated SVG never bypasses sanitization. Malformed
// or partial sources produce a fallback <pre> instead of throwing.

import type { Mermaid, MermaidConfig } from 'mermaid'
import { decodeMermaidSource } from '@/lib/markdown/plugins/mermaidPlaceholder'

export interface MermaidController {
  hydrate(root: HTMLElement): void
  dispose(): void
}

export function useMermaid(): MermaidController {
  return {
    hydrate(root) {
      schedule(root)
    },
    dispose() {
      clearPending()
    },
  }
}

// ---------------------------------------------------------------------------

const mermaidOptions: MermaidConfig = {
  startOnLoad: false,
  securityLevel: 'strict',
}

let mermaidPromise: Promise<Mermaid> | null = null

function getMermaid(): Promise<Mermaid> {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then((mod) => {
      mod.default.initialize(mermaidOptions)
      return mod.default
    })
  }
  return mermaidPromise
}

// Render cache: source hash -> last production of that source. The SVG carries
// a base id that all internal refs are prefixed with, so reuse mints a fresh id.
const renderCache = new Map<string, { renderId: string; svg: string }>()

let seq = 0

function hashSource(s: string): string {
  let h = 5381
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) + h + s.charCodeAt(i)) | 0
  }
  return (h >>> 0).toString(36)
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

// Coalesce hydration into a single pass per tick and serialize overlapping
// async renders.
let planTimer: ReturnType<typeof setTimeout> | null = null
let pendingRoot: HTMLElement | null = null
let running: Promise<void> = Promise.resolve()

function clearPending(): void {
  if (planTimer != null) clearTimeout(planTimer)
  planTimer = null
  pendingRoot = null
}

function schedule(root: HTMLElement): void {
  pendingRoot = root
  if (planTimer != null) return
  planTimer = setTimeout(() => {
    planTimer = null
    const target = pendingRoot
    pendingRoot = null
    if (!target || !target.isConnected) return
    running = running.then(() => hydrateNow(target)).catch(() => {})
  }, 0)
}

async function hydrateNow(root: HTMLElement): Promise<void> {
  const placeholders = Array.from(
    root.querySelectorAll<HTMLElement>('.mermaid-placeholder[data-mermaid-source]'),
  )
  if (placeholders.length === 0) return
  for (const el of placeholders) {
    if (el.dataset.rendered === 'done') continue
    const source = decodeMermaidSource(el.getAttribute('data-mermaid-source') ?? '')
    if (source.trim() === '') continue
    const hash = hashSource(source)
    if (el.dataset.attempted === hash) continue
    el.dataset.attempted = hash

    try {
      const cached = renderCache.get(hash)
      let renderId: string
      let svg: string
      if (cached) {
        renderId = `mermaid-${seq++}`
        svg = cached.svg.split(cached.renderId).join(renderId)
      } else {
        renderId = `mermaid-${seq++}`
        const mermaid = await getMermaid()
        const result = await mermaid.render(renderId, source)
        svg = result.svg
        renderCache.set(hash, { renderId, svg })
      }

      const host = document.createElement('div')
      host.className = 'mermaid-render'
      host.dataset.rendered = 'done'
      host.innerHTML = svg
      el.replaceWith(host)
    } catch {
      el.dataset.rendered = 'error'
      el.innerHTML =
        `<span class="mermaid-error-chip">mermaid</span>` +
        `<pre class="mermaid-error-code">${escapeHtml(source)}</pre>`
    }
  }
}