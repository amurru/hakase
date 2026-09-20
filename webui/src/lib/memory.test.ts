import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  fetchMemory,
  forgetMemoryNote,
  groupByCategory,
  scopeLabel,
  type MemoryList,
} from './memory'

function stubFetch(response: unknown, status = 200) {
  const fn = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(response), {
      status,
      headers: { 'Content-Type': 'application/json' },
    }),
  )
  vi.stubGlobal('fetch', fn)
  return fn
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('memory api wrappers', () => {
  it('fetchMemory GETs /memory', async () => {
    const list: MemoryList = {
      notes: [
        {
          id: 'mem_a1',
          category: 'user',
          content: 'prefers terse answers',
          created_at: '2026-09-20T10:00:00Z',
          updated_at: '2026-09-20T10:00:00Z',
        },
      ],
      store_path: '/home/u/.hakase/memory/notes.json',
    }
    const f = stubFetch(list)
    await expect(fetchMemory()).resolves.toEqual(list)
    expect(f).toHaveBeenCalledWith('/api/memory', expect.objectContaining({ headers: expect.anything() }))
  })

  it('forgetMemoryNote DELETEs /memory/{id}', async () => {
    const f = stubFetch({ forgotten: true })
    await expect(forgetMemoryNote('mem_a1')).resolves.toEqual({ forgotten: true })
    expect(f).toHaveBeenCalledWith(
      '/api/memory/mem_a1',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('forgetMemoryNote encodes the id', async () => {
    const f = stubFetch({ forgotten: true })
    await forgetMemoryNote('mem needs/encoding')
    expect(f).toHaveBeenCalledWith(
      '/api/memory/mem%20needs%2Fencoding',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })
})

describe('memory view helpers', () => {
  const notes = [
    { id: '1', category: 'lesson', content: 'a', created_at: '', updated_at: '' },
    { id: '2', category: 'user', content: 'b', project: '/repo', created_at: '', updated_at: '' },
    { id: '3', category: 'bogus', content: 'ignored', created_at: '', updated_at: '' },
  ] as never[]

  it('scopeLabel distinguishes global from project notes', () => {
    expect(scopeLabel({ project: '' })).toBe('global')
    expect(scopeLabel({ project: '/repo' })).toBe('/repo')
  })

  it('groupByCategory buckets by the fixed enum and drops unknown categories', () => {
    const groups = groupByCategory(notes)
    expect(groups.lesson.map((n) => n.id)).toEqual(['1'])
    expect(groups.user.map((n) => n.id)).toEqual(['2'])
    expect(groups.feedback).toEqual([])
    expect(groups.project).toEqual([])
  })
})
