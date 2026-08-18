// Mermaid placeholder plugin for markdown-it.
//
// markdown-it rendering is synchronous, but mermaid's render API is async, so
// a ` ```mermaid ` fence is emitted as a placeholder div carrying the raw
// source in a data attribute:
//
//   <div class="mermaid-placeholder" data-mermaid-source="..."></div>
//
// The source is base64-encoded (UTF-8) so it survives DOMPurify: attribute
// values containing `-->` (common in mermaid, e.g. `A-->B`) are stripped by
// its attribute sanitizer. A composable (useMermaid) decodes and hydrates the
// placeholder into an SVG after the component mounts/updates.

import MarkdownIt from 'markdown-it'

type Token = MarkdownIt.Token

/**
 * encodeMermaidSource base64-encodes a mermaid source as UTF-8, safe to store
 * in a data attribute and pass through DOMPurify.
 */
export function encodeMermaidSource(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
}

/**
 * decodeMermaidSource reverses encodeMermaidSource.
 */
export function decodeMermaidSource(b64: string): string {
  const binary = atob(b64)
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

export function mermaidPlaceholder(): MarkdownIt.PluginSimple {
  return function mermaidPlaceholderPlugin(md: MarkdownIt): void {
    const originalFence = md.renderer.rules.fence

    md.renderer.rules.fence = (
      tokens: Token[],
      idx: number,
      options: MarkdownIt.Options,
      env: any,
      self: MarkdownIt.Renderer,
    ): string => {
      const token = tokens[idx]
      const info = token.info ? token.info.trim().split(/\s+/)[0] : ''
      if (info === 'mermaid') {
        return `<div class="mermaid-placeholder" data-mermaid-source="${encodeMermaidSource(token.content.trim())}"></div>`
      }
      if (originalFence) {
        return originalFence(tokens, idx, options, env, self)
      }
      return ''
    }
  }
}