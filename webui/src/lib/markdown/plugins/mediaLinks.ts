// Media-link rewriting plugin for markdown-it.
//
// Markdown has no syntax for <video>/<audio> and the renderer runs with
// `html: false`, so links (and bare image links) whose URL ends in a known
// media extension are rewritten to the matching inline element:
//
//   - .png/.jpg/.jpeg/.gif/.webp/.svg/.avif/.bmp/.ico -> <img>
//   - .mp4/.webm/.mov/.m4v/.ogg/.ogv               -> <video controls>
//   - .mp3/.wav/.flac/.m4a/.aac/.opus/.oga          -> <audio controls>
//
// Workspace-relative URLs (no scheme, no leading slash, no fragment) are
// rewritten to the authenticated inline file route so local artifacts render.
// Absolute http(s) and data: URLs pass through untouched (CSP permitting).
// Unknown extensions and non-media links are left alone (no behavior change).

import MarkdownIt from 'markdown-it'

type Token = MarkdownIt.Token

const IMAGE_EXT = new Set([
  '.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.avif', '.bmp', '.ico',
])
const VIDEO_EXT = new Set(['.mp4', '.webm', '.mov', '.m4v', '.ogg', '.ogv'])
const AUDIO_EXT = new Set(['.mp3', '.wav', '.flac', '.m4a', '.aac', '.opus', '.oga'])

type MediaKind = 'image' | 'video' | 'audio'

// kindFor detects the media kind from a URL's extension. Returns null when the
// URL is not a known media type.
function kindFor(src: string): MediaKind | null {
  const path = src.split(/[?#]/)[0] ?? ''
  const dot = path.lastIndexOf('.')
  if (dot < 0) return null
  const ext = path.slice(dot).toLowerCase()
  if (IMAGE_EXT.has(ext)) return 'image'
  if (VIDEO_EXT.has(ext)) return 'video'
  if (AUDIO_EXT.has(ext)) return 'audio'
  return null
}

// isWorkspaceRelative reports whether src is a workspace-relative path: it has
// no URI scheme (http:, data:, mailto:, ...), no leading slash, and is not a
// fragment-only link.
function isWorkspaceRelative(src: string): boolean {
  return !/^\s*(?:[a-zA-Z][a-zA-Z0-9+.\-]*:|\/|#)/.test(src)
}

// isSameOriginOrDataUrl checks if a URL is same-origin (no external protocol)
// or a data: URL. Used to filter audio/video to only these types.
function isSameOriginOrDataUrl(src: string): boolean {
  // Protocol-relative URLs (//host/path) resolve against the page's scheme to
  // an external origin; treat them as external so they stay plain links.
  if (src.startsWith('//')) {
    return false
  }
  // No protocol = relative/same-origin
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(src)) {
    return true
  }
  // data: URLs are safe
  if (src.startsWith('data:')) {
    return true
  }
  // All other protocols are external and should remain as links
  return false
}

// rewriteSrc rewrites a workspace-relative src/href to the authenticated inline
// file route; everything else passes through unchanged. Preserves fragments and
// query parameters.
function rewriteSrc(src: string): string {
  if (!isWorkspaceRelative(src)) return src
  
  // Split into path, query, and fragment
  const urlObj = new URL(src, 'file://placeholder') // Use placeholder base for parsing
  // URL resolution prefixes relative paths with "/" (pathname of the
  // placeholder base); strip it so the encoded path stays workspace-relative.
  const clean = urlObj.pathname.replace(/^\//, '')
  const query = urlObj.search
  const fragment = urlObj.hash
  
  let result = `/api/files/inline?path=${encodeURIComponent(clean)}`
  if (query !== '') {
    result += `&${query.slice(1)}`
  }
  if (fragment !== '') {
    result += `#${fragment.slice(1)}`
  }
  
  return result
}

function htmlEscape(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

function collectText(tokens: Token[], start: number, end: number): string {
  let out = ''
  for (let i = start; i < end; i++) {
    out += tokens[i].content ?? ''
  }
  return out.trim()
}

// makeMediaTag builds a synthetic media_tag token carrying { kind, src, alt }.
function makeMediaTag(kind: MediaKind, src: string, alt: string): Token {
  const token = new MarkdownIt.Token('media_tag', '', 0)
  token.meta = { kind, src, alt }
  return token
}

export function mediaLinks(): MarkdownIt.PluginSimple {
  return function mediaLinksPlugin(md: MarkdownIt): void {
    md.core.ruler.push('media-links', (state: MarkdownIt.Core.StateCore) => {
      for (const block of state.tokens) {
        if (block.type !== 'inline' || !block.children) continue
        const children = block.children
        for (let j = 0; j < children.length; j++) {
          const tok = children[j]

          if (tok.type === 'image') {
            const src = tok.attrGet('src') ?? ''
            const kind = kindFor(src)
            if (!kind) continue
            if (kind === 'image') {
              tok.attrSet('src', rewriteSrc(src))
              continue
            }
            // A video/audio extension on an image token -> convert to media_tag
            // only if same-origin or data URL (remote media stays as image)
            if (!isSameOriginOrDataUrl(src)) continue
            const alt = collectText(tok.children ?? [], 0, (tok.children ?? []).length)
            children.splice(j, 1, makeMediaTag(kind, rewriteSrc(src), alt))
            continue
          }

          if (tok.type === 'link_open') {
            const href = tok.attrGet('href') ?? ''
            const kind = kindFor(href)
            if (!kind) continue
            // Only convert video/audio if same-origin or data URL (remote media stays as link)
            if ((kind === 'video' || kind === 'audio') && !isSameOriginOrDataUrl(href)) continue
            // Find the matching link_close to splice the whole [text](url).
            let closeIdx = -1
            for (let k = j + 1; k < children.length; k++) {
              if (children[k].type === 'link_close') {
                closeIdx = k
                break
              }
            }
            if (closeIdx < 0) continue
            const alt = collectText(children, j + 1, closeIdx)
            children.splice(j, closeIdx - j + 1, makeMediaTag(kind, rewriteSrc(href), alt))
          }
        }
      }
    })

    md.renderer.rules.media_tag = (tokens: Token[], idx: number): string => {
      const { kind, src, alt } = tokens[idx].meta as {
        kind: MediaKind
        src: string
        alt: string
      }
      const url = htmlEscape(src)
      const altText = htmlEscape(alt)
      if (kind === 'video') return `<video controls src="${url}"></video>`
      if (kind === 'audio') return `<audio controls src="${url}"></audio>`
      return `<img src="${url}" alt="${altText}" />`
    }
  }
}