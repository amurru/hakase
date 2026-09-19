// Pure layout math for the chat message navigation rail. Kept
// framework-free and unit-tested like lib/canvas.ts.

export interface ScrollMetrics {
  scrollTop: number
  clientHeight: number
  scrollHeight: number
}

export interface RailAnchor {
  id: string
  /** px from content start to the message element's top edge */
  anchorTop: number
  anchorHeight: number
}

export interface RailTick {
  id: string
  /** kept on the tick so active-tick tracking needs no DOM access */
  anchorTop: number
  /** scrollTop that best centers the message in the viewport */
  scrollTarget: number
  /** tick center position in rail pixel space */
  y: number
}

/** RailTick plus the tooltip payload the chat view attaches per message. */
export interface RailTickVM extends RailTick {
  preview: string
  timestamp: number
}

/**
 * Vertical distance between neighboring strips. The ticks form a compact,
 * vertically centered stack (ZCode-style) that never stretches with the
 * window; strip position carries no scroll information — hover, the active
 * highlight, and clicks do.
 */
export const RAIL_PITCH = 14
/** Don't render the rail for short conversations. */
export const RAIL_MIN_MESSAGES = 3
/** Required scrollable overflow (px) before the rail earns its place. */
export const RAIL_SCROLL_BUFFER = 80

// A turn counts as "being read" once its top crosses the upper third of the
// viewport.
const READING_LINE = 1 / 3

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi)
}

/**
 * Maps anchors to rail ticks: a compact strip stack at a fixed pitch,
 * centered in the rail. Each tick's scrollTarget stays scroll-truthful, so
 * clicks jump to where the message really lives; `anchorTop` is kept for
 * active-tick tracking. Very long sessions shrink the pitch (down to a 60%
 * rail span) so the stack never outgrows the rail.
 */
export function layoutRail(
  anchors: RailAnchor[],
  metrics: ScrollMetrics,
  railHeight: number,
): RailTick[] {
  if (anchors.length === 0) return []
  const steps = anchors.length - 1
  const pitch = steps > 0 ? Math.min(RAIL_PITCH, (railHeight * 0.6) / steps) : RAIL_PITCH
  const first = railHeight / 2 - (pitch * steps) / 2
  const maxScroll = Math.max(0, metrics.scrollHeight - metrics.clientHeight)
  return anchors.map((a, i) => ({
    id: a.id,
    anchorTop: a.anchorTop,
    y: first + i * pitch,
    scrollTarget: clamp(a.anchorTop + a.anchorHeight / 2 - metrics.clientHeight / 2, 0, maxScroll),
  }))
}

/** Index of the tick whose message is currently being read, or -1. */
export function activeTickIndex(ticks: RailTick[], metrics: ScrollMetrics): number {
  const line = metrics.scrollTop + metrics.clientHeight * READING_LINE
  for (let i = ticks.length - 1; i >= 0; i--) {
    if (ticks[i].anchorTop <= line) return i
  }
  return -1
}

/**
 * First-line preview for the hover tooltip: user prompts render as plain
 * pre-wrapped text, so this only flattens whitespace and truncates.
 */
export function railPreviewText(content: string, maxLen = 140): string {
  const text = content.replace(/\s+/g, ' ').trim()
  if (!text) return ''
  return text.length > maxLen ? text.slice(0, maxLen).trimEnd() + '…' : text
}

/** The rail only earns its place on long, actually-scrollable transcripts. */
export function shouldShowRail(userMessageCount: number, metrics: ScrollMetrics): boolean {
  return (
    userMessageCount >= RAIL_MIN_MESSAGES &&
    metrics.scrollHeight > metrics.clientHeight + RAIL_SCROLL_BUFFER
  )
}
