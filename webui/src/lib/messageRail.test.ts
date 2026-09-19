import { describe, it, expect } from 'vitest'
import {
  layoutRail,
  activeTickIndex,
  railPreviewText,
  shouldShowRail,
  type RailAnchor,
  type ScrollMetrics,
} from './messageRail'

function metrics(scrollTop = 0, clientHeight = 400, scrollHeight = 2000): ScrollMetrics {
  return { scrollTop, clientHeight, scrollHeight }
}

function anchor(id: string, anchorTop: number, anchorHeight = 40): RailAnchor {
  return { id, anchorTop, anchorHeight }
}

describe('layoutRail', () => {
  // Ticks form a compact stack with a fixed 14px pitch, vertically centered
  // in the rail — a ZCode-like strip cluster that never stretches with the
  // window.
  const RAIL_H = 564

  it('stacks ticks at a fixed pitch, centered in the rail', () => {
    const ticks = layoutRail(
      [anchor('a', 16), anchor('b', 1040, 120), anchor('c', 1850, 100)],
      metrics(),
      RAIL_H,
    )
    expect(ticks.map((t) => t.y)).toEqual([268, 282, 296])
    expect(ticks.map((t) => t.scrollTarget)).toEqual([0, 900, 1600])
  })

  it('places a lone tick at the vertical center of the rail', () => {
    const ticks = layoutRail([anchor('a', 1040, 120)], metrics(), RAIL_H)
    expect(ticks[0].y).toBe(282)
    expect(ticks[0].scrollTarget).toBe(900)
  })

  it('keeps the stack compact regardless of rail height', () => {
    const ticks = layoutRail(
      [anchor('a', 16), anchor('b', 1040, 120), anchor('c', 1850, 100)],
      metrics(),
      300,
    )
    expect(ticks.map((t) => t.y)).toEqual([136, 150, 164])
  })

  it('keeps dense sessions ordered inside the centered stack', () => {
    const anchors = Array.from({ length: 20 }, (_, i) => anchor(`m${i}`, 100 + i * 200))
    const ticks = layoutRail(anchors, metrics(0, 400, 5000), RAIL_H)
    const ys = ticks.map((t) => t.y)
    expect(ys[ys.length - 1] - ys[0]).toBe(19 * 14)
    for (let i = 1; i < ys.length; i++) {
      expect(ys[i]).toBeGreaterThan(ys[i - 1])
    }
    expect((ys[0] + ys[ys.length - 1]) / 2).toBe(RAIL_H / 2)
  })

  it('shrinks the pitch when the stack would outgrow the rail', () => {
    const anchors = Array.from({ length: 60 }, (_, i) => anchor(`m${i}`, 50 + i * 50))
    const ticks = layoutRail(anchors, metrics(0, 400, 40000), 300)
    const ys = ticks.map((t) => t.y)
    expect(Math.round(ys[ys.length - 1] - ys[0])).toBe(180)
    expect((ys[0] + ys[ys.length - 1]) / 2).toBe(150)
  })

  it('returns an empty list without anchors', () => {
    expect(layoutRail([], metrics(), RAIL_H)).toEqual([])
  })
})

describe('activeTickIndex', () => {
  const anchors = [anchor('a', 100), anchor('b', 500), anchor('c', 900)]

  it('selects the last message above the reading line as you scroll', () => {
    const ticks = layoutRail(anchors, metrics(0, 300, 3000), 100)
    const at = (scrollTop: number): number =>
      activeTickIndex(ticks, metrics(scrollTop, 300, 3000))
    expect(at(0)).toBe(0)
    expect(at(500)).toBe(1)
    expect(at(2600)).toBe(2)
  })

  it('returns -1 when no message has crossed the reading line', () => {
    const ticks = layoutRail([anchor('far', 500)], metrics(0, 300, 3000), 100)
    expect(activeTickIndex(ticks, metrics(0, 300, 3000))).toBe(-1)
  })

  it('returns -1 without ticks', () => {
    expect(activeTickIndex([], metrics())).toBe(-1)
  })
})

describe('railPreviewText', () => {
  it('collapses newlines and repeated whitespace into one line', () => {
    expect(railPreviewText('hello\n  world\t\nagain')).toBe('hello world again')
  })

  it('truncates long text with an ellipsis', () => {
    const preview = railPreviewText('a'.repeat(200), 140)
    expect(preview).toBe('a'.repeat(140) + '…')
  })

  it('returns an empty string for blank content', () => {
    expect(railPreviewText('  \n  ')).toBe('')
  })
})

describe('shouldShowRail', () => {
  it('hides with fewer than three user messages', () => {
    expect(shouldShowRail(2, metrics())).toBe(false)
  })

  it('hides when the content barely scrolls', () => {
    expect(shouldShowRail(5, metrics(0, 400, 440))).toBe(false)
  })

  it('shows for a scrollable conversation with enough turns', () => {
    expect(shouldShowRail(5, metrics())).toBe(true)
  })
})
