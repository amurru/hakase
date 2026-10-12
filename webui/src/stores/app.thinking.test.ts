import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { THINKING_LEVELS, useAppStore } from './app'

const KEY = 'hakase-thinking-level'

describe('thinking level store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.removeItem(KEY)
  })

  it('defaults to the server default (no override)', () => {
    const store = useAppStore()
    expect(store.thinkingLevel).toBe('')
    expect(store.thinkingLevelLabel(store.thinkingLevel)).toBe('Default')
  })

  it('accepts canonical levels and persists them', () => {
    const store = useAppStore()
    store.setThinkingLevel('high')
    expect(store.thinkingLevel).toBe('high')
    expect(store.thinkingLevelLabel('high')).toBe('High')
    expect(localStorage.getItem(KEY)).toBe('high')
  })

  it('resets unknown values to the server default', () => {
    const store = useAppStore()
    store.setThinkingLevel('uber')
    expect(store.thinkingLevel).toBe('')
    expect(localStorage.getItem(KEY)).toBe('')
  })

  it('restores the persisted selection across reloads', () => {
    localStorage.setItem(KEY, 'low')
    setActivePinia(createPinia())
    expect(useAppStore().thinkingLevel).toBe('low')
  })

  it('ignores a persisted unknown value', () => {
    localStorage.setItem(KEY, 'maximum')
    setActivePinia(createPinia())
    // 'maximum' is a server-side alias, never a menu value: the store falls
    // back to the default rather than sending something it cannot display.
    expect(useAppStore().thinkingLevel).toBe('')
  })

  it('exposes the full canonical menu', () => {
    expect(THINKING_LEVELS.map((o) => o.value)).toEqual(
      ['', 'off', 'minimal', 'low', 'medium', 'high'],
    )
  })
})
