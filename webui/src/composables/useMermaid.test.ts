import { afterEach, describe, expect, it, vi } from 'vitest'
import { useMermaid } from './useMermaid'

// Verifies the T2C.3 acceptance criterion: calling hydrate() on a div with a
// known-good mermaid source produces a rendered SVG (not a fallback).
//
// hydrate() coalesces on a setTimeout(0) plan timer, lazily imports the
// mermaid ESM bundle, then renders — all async and sensitive to machine load.
// Assert by polling with a real deadline; a fixed sleep budget flaked under
// parallel vitest workers.

function htmlWithPlaceholder(source: string): string {
  return (
    `<div><div class="mermaid-placeholder" data-mermaid-source="` +
    btoa(source) +
    `"></div></div>`
  )
}

describe('useMermaid hydrate', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it(
    'renders a known-good source to an SVG',
    async () => {
      document.body.innerHTML = htmlWithPlaceholder('graph LR\nA-->B')

      const root = document.body.querySelector('div') as HTMLElement
      useMermaid().hydrate(root)
      await vi.waitFor(
        () => {
          const host = root.querySelector('.mermaid-render') as HTMLElement | null
          expect(host).not.toBeNull()
          expect(host!.querySelector('svg')).not.toBeNull()
          expect(host!.dataset.rendered).toBe('done')
        },
        { timeout: 15_000, interval: 25 },
      )
    },
    20_000,
  )

  it(
    'emits the fallback for a malformed source',
    async () => {
      document.body.innerHTML = htmlWithPlaceholder('!!!broken')

      const root = document.body.querySelector('div') as HTMLElement
      useMermaid().hydrate(root)
      await vi.waitFor(
        () => {
          const host = root.querySelector('.mermaid-placeholder') as HTMLElement | null
          expect(host).not.toBeNull()
          expect(host!.dataset.rendered).toBe('error')
          expect(host!.querySelector('.mermaid-error-code')).not.toBeNull()
        },
        { timeout: 15_000, interval: 25 },
      )
    },
    20_000,
  )
})
