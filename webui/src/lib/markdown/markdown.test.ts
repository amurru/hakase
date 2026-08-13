// Unit tests for the markdown rendering pipeline (spec MD-008 / task T4.1).
//
// Covers: headings/code/tables/lists, inline math, display math, fenced math,
// mermaid placeholder emission, media rewrites (img/video/audio, local,
// external, data), and DOMPurify sanitization probes.

import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './index'
import { decodeMermaidSource } from './plugins/mermaidPlaceholder'

describe('renderMarkdown', () => {
  it('renders headings', () => {
    const html = renderMarkdown('# Title\n\n## Sub')
    expect(html).toContain('<h1>Title</h1>')
    expect(html).toContain('<h2>Sub</h2>')
  })

  it('renders fenced code with highlight.js classes', () => {
    const html = renderMarkdown('```js\nconst x = 1\n```')
    expect(html).toContain('<pre class="hljs">')
    expect(html).toContain('language-js')
    expect(html).toContain('const')
  })

  it('escapes raw HTML inside fenced code', () => {
    const html = renderMarkdown('```html\n<div>x</div>\n```')
    expect(html).toContain('&lt;')
    expect(html).not.toContain('<div>x</div>')
  })

  it('renders tables', () => {
    const html = renderMarkdown('| a | b |\n|---|---|\n| 1 | 2 |')
    expect(html).toContain('<table>')
    expect(html).toContain('<th>a</th>')
    expect(html).toContain('<td>1</td>')
  })

  it('renders lists', () => {
    const html = renderMarkdown('- one\n- two')
    expect(html).toContain('<ul>')
    expect(html).toContain('<li>one</li>')
    expect(html).toContain('<li>two</li>')
  })

  it('renders inline math as KaTeX', () => {
    const html = renderMarkdown('Euler: $e^{i\\pi}+1=0$')
    expect(html).toContain('class="katex"')
    expect(html).toContain('katex-mathml')
  })

  it('renders display math as KaTeX', () => {
    const html = renderMarkdown('$$x^2 + y^2 = z^2$$')
    expect(html).toContain('class="katex')
    expect(html).toContain('katex-display')
  })

  it('renders fenced math blocks', () => {
    const html = renderMarkdown('```math\n\\int_0^1 x \\, dx\n```')
    expect(html).toContain('class="katex')
    expect(html).toContain('katex-display')
  })

  it('does not throw on malformed math (throwOnError: false)', () => {
    const html = renderMarkdown('$\\notacommand{}$')
    expect(html).toBeTypeOf('string')
  })

  it('emits a mermaid placeholder div with a base64 source that decodes back', () => {
    const html = renderMarkdown('```mermaid\ngraph LR\nA-->B\n```')
    expect(html).toContain('class="mermaid-placeholder"')
    const m = html.match(/data-mermaid-source="([^"]+)"/)
    expect(m).not.toBeNull()
    expect(decodeMermaidSource(m![1]!)).toBe('graph LR\nA-->B')
  })

  it('does not treat a non-mermaid fence as a placeholder', () => {
    const html = renderMarkdown('```txt\nplain\n```')
    expect(html).not.toContain('mermaid-placeholder')
  })

  describe('media rewrites', () => {
    it('rewrites a workspace-relative image path to the inline route', () => {
      const html = renderMarkdown('![chart](chart.png)')
      expect(html).toContain('<img src="/api/files/inline?path=chart.png"')
    })

    it('rewrites a workspace-relative video link to <video>', () => {
      const html = renderMarkdown('[clip](clip.mp4)')
      expect(html).toContain('<video controls')
      expect(html).toContain('src="/api/files/inline?path=clip.mp4"')
    })

    it('rewrites a workspace-relative audio link to <audio>', () => {
      const html = renderMarkdown('[tone](tone.mp3)')
      expect(html).toContain('<audio controls')
      expect(html).toContain('src="/api/files/inline?path=tone.mp3"')
    })

    it('leaves external image URLs untouched', () => {
      const html = renderMarkdown('![x](https://example.com/a.png)')
      expect(html).toContain('<img src="https://example.com/a.png"')
      expect(html).not.toContain('/api/files/inline')
    })

    it('leaves data: image URLs untouched', () => {
      const html = renderMarkdown('![x](data:image/png;base64,AAAB)')
      expect(html).toContain('src="data:image/png;base64,AAAB"')
    })

    it('does not rewrite an absolute workspace path (leading slash)', () => {
      const html = renderMarkdown('![x](/abs/path.png)')
      expect(html).toContain('src="/abs/path.png"')
      expect(html).not.toContain('/api/files/inline')
    })

    it('leaves non-media links alone', () => {
      const html = renderMarkdown('[docs](https://example.com/doc)')
      expect(html).toContain('href="https://example.com/doc"')
      expect(html).not.toContain('media_tag')
    })
  })

  describe('sanitization', () => {
    it('never emits a live <script> element (html:false escapes it)', () => {
      const html = renderMarkdown('<script>alert(1)</script>hello')
      expect(html).not.toContain('<script')
      expect(html).toContain('&lt;script&gt;')
      expect(html).toContain('hello')
    })

    it('never emits an element carrying onerror (escaped as text)', () => {
      const html = renderMarkdown('<img src="x.png" onerror="alert(1)">')
      expect(html).not.toContain('<img')
      expect(html).toContain('&lt;img')
    })

    it('strips <iframe> (frames disabled per Phase 0)', () => {
      const html = renderMarkdown('<iframe src="https://evil.example"></iframe>')
      expect(html).not.toContain('<iframe')
    })

    it('keeps inline style inside .katex output', () => {
      const html = renderMarkdown('$x$')
      expect(html).toContain('class="katex"')
      expect(html).toContain('style="')
    })

    it('keeps mermaid data-source after sanitization', () => {
      const html = renderMarkdown('```mermaid\ngraph LR\nA-->B\n```')
      expect(html).toContain('data-mermaid-source=')
    })
  })

  describe('link hardening', () => {
    it('adds target/rel to absolute external links', () => {
      const html = renderMarkdown('[link](https://example.com/page)')
      expect(html).toContain('target="_blank"')
      expect(html).toContain('rel="noopener noreferrer"')
    })

    it('leaves relative links alone', () => {
      const html = renderMarkdown('[local](./page)')
      expect(html).toContain('href="./page"')
      expect(html).not.toContain('target="_blank"')
    })
  })

  it('returns an empty string for empty input', () => {
    expect(renderMarkdown('')).toBe('')
  })
})
