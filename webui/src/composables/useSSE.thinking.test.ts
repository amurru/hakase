import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { apiFetch } from '@/lib/api'
import { useSSE } from './useSSE'

vi.mock('@/lib/api', () => ({
  apiFetch: vi.fn().mockResolvedValue({}),
  apiGet: vi.fn().mockResolvedValue({}),
  ApiError: class ApiError extends Error {},
}))

// Minimal EventSource stub: sendMessage connects after the POST when no
// stream is open.
class StubEventSource {
  static CLOSED = 2
  readyState = 1
  onopen: (() => void) | null = null
  onerror: ((e: unknown) => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  addEventListener() {}
  removeEventListener() {}
  close() {
    this.readyState = StubEventSource.CLOSED
  }
}

describe('sendMessage thinking_level', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = StubEventSource
    vi.mocked(apiFetch).mockClear()
  })

  afterEach(() => {
    delete (globalThis as unknown as { EventSource?: unknown }).EventSource
  })

  it('sends thinking_level when an effort override is selected', async () => {
    const sse = useSSE(() => 'sid-1')
    await sse.sendMessage('hi', undefined, 'high')
    expect(apiFetch).toHaveBeenCalledOnce()
    const [, opts] = vi.mocked(apiFetch).mock.calls[0] as [string, { body: Record<string, unknown> }]
    expect(opts.body.thinking_level).toBe('high')
    expect(opts.body.content).toBe('hi')
  })

  it('omits thinking_level on the server default', async () => {
    const sse = useSSE(() => 'sid-1')
    await sse.sendMessage('hi')
    expect(apiFetch).toHaveBeenCalledOnce()
    const [, opts] = vi.mocked(apiFetch).mock.calls[0] as [string, { body: Record<string, unknown> }]
    expect('thinking_level' in opts.body).toBe(false)
  })
})
