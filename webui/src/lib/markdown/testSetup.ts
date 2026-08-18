// jsdom test setup for the markdown pipeline.
//
// mermaid's render path needs a couple of APIs jsdom does not implement:
//   - SVGElement.prototype.getBBox (mermaid measures text in SVG)
//   - CSSStyleSheet.replaceSync (jsdom implements it; happy-dom does not)
//
// The polyfills are only installed when the surrounding globals exist, so the
// same file is safe to import in any vitest environment.

type GetBBox = () => { x: number; y: number; width: number; height: number }

declare global {
  interface SVGElement {
    getBBox: GetBBox
  }
}

if (typeof window !== 'undefined' && window.SVGElement) {
  if (!window.SVGElement.prototype.getBBox) {
    window.SVGElement.prototype.getBBox = () => ({
      x: 0,
      y: 0,
      width: 120,
      height: 30,
    })
  }
}

export {}
