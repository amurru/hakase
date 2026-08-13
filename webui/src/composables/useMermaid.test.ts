import { afterEach, describe, expect, it } from 'vitest'
import { useMermaid } from './useMermaid'

// Verifies the T2C.3 acceptance criterion: calling hydrate() on a div with a
// known-good mermaid source produces a rendered SVG (not a fallback).

function flush(times = 20): Promise<void> {
  const wait = (): Promise<void> =>
    new Promise((resolve) => setTimeout(resolve, 10))
  let p: Promise<void> = Promise.resolve()
  for (let i = 0; i < times; i++) p = p.then(wait)
  return p
}

describe('useMermaid hydrate', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('renders a known-good source to an SVG', async () => {
    document.body.innerHTML =
      `<div><div class="mermaid-placeholder" data-mermaid-source="` +
      btoa('graph LR\nA-->B') +
      `"></div></div>`

    const root = document.body.querySelector('div') as HTMLElement
    useMermaid().hydrate(root)
    await flush()

    const host = root.querySelector('.mermaid-render') as HTMLElement | null
    expect(host).not.toBeNull()
    expect(host!.querySelector('svg')).not.toBeNull()
    expect(host!.dataset.rendered).toBe('done')
  })

  it('emits the fallback for a malformed source', async () => {
    document.body.innerHTML =
      `<div><div class="mermaid-placeholder" data-mermaid-source="` +
      btoa('!!!broken') +
      `"></div></div>`

    const root = document.body.querySelector('div') as HTMLElement
    useMermaid().hydrate(root)
    await flush()

    const host = root.querySelector('.mermaid-placeholder') as HTMLElement | null
    expect(host).not.toBeNull()
    expect(host!.dataset.rendered).toBe('error')
    expect(host!.querySelector('.mermaid-error-code')).not.toBeNull()
  })
})
