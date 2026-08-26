import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { toast } from 'vue-sonner'
import { useSSE } from './useSSE'
import { parseSidekickEvent, parseSidekickCommand, sidekickSeverityClass } from '@/lib/sidekick'

// Captures addEventListener registrations so tests can dispatch synthetic SSE
// frames the same way the real EventSource would.
class FakeEventSource {
  url: string
  handlers: Record<string, Array<(e: { data: string }) => void>> = {}
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  static CLOSED = 2
  readyState = 0

  constructor(url: string) {
    this.url = url
    ;(globalThis as unknown as { __lastES: FakeEventSource }).__lastES = this
  }

  addEventListener(type: string, cb: (e: { data: string }) => void) {
    ;(this.handlers[type] ||= []).push(cb)
  }

  removeEventListener() {}

  close() {
    this.readyState = FakeEventSource.CLOSED
  }

  dispatch(type: string, data: string) {
    ;(this.handlers[type] || []).forEach((h) => h({ data }))
  }
}

describe('sidekick SSE handling', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource
    vi.restoreAllMocks()
  })

  afterEach(() => {
    delete (globalThis as unknown as { EventSource?: unknown }).EventSource
  })

  it('creates a note entry on a sidekick SSE dispatch without notifying', () => {
    const toastInfo = vi.spyOn(toast, 'info').mockImplementation(() => '')
    vi.spyOn(toast, 'warning').mockImplementation(() => '')
    vi.spyOn(toast, 'error').mockImplementation(() => '')
    vi.spyOn(toast, 'success').mockImplementation(() => '')

    const sse = useSSE(() => 'sid-1')
    sse.connect('sid-1')

    const es = (globalThis as unknown as { __lastES: FakeEventSource }).__lastES
    es.dispatch('sidekick', JSON.stringify({ severity: 'warning', text: 'double-check the API key' }))

    expect(sse.sidekickNotes.value).toHaveLength(1)
    expect(sse.sidekickNotes.value[0].severity).toBe('warning')
    expect(sse.sidekickNotes.value[0].text).toBe('double-check the API key')
    // Decision Q3: advisory notes are quiet chips only, never notifications.
    expect(toastInfo).not.toHaveBeenCalled()
    expect(toast.warning).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('drops malformed sidekick payloads without error', () => {
    const sse = useSSE(() => 'sid-2')
    sse.connect('sid-2')

    const es = (globalThis as unknown as { __lastES: FakeEventSource }).__lastES
    es.dispatch('sidekick', 'not-json')
    expect(sse.sidekickNotes.value).toHaveLength(0)
  })

  it('parses a sidekick payload and maps severity to a quiet chip class', () => {
    const note = parseSidekickEvent('{"severity":"critical","text":"x"}')
    expect(note?.severity).toBe('critical')
    expect(note?.text).toBe('x')
    expect(sidekickSeverityClass('critical')).toContain('red')
    expect(sidekickSeverityClass('warning')).toContain('amber')
    expect(sidekickSeverityClass('suggestion')).toContain('emerald')
    expect(sidekickSeverityClass('info')).toContain('sky')
    // Unknown severities fall back to the info (sky) class.
    expect(sidekickSeverityClass('bogus')).toContain('sky')
  })
})

describe('/sidekick chat command', () => {
  it('detects the command and extracts the question', () => {
    expect(parseSidekickCommand('hello')).toBeNull()
    expect(parseSidekickCommand('/sidekickextra')).toBeNull()
    expect(parseSidekickCommand('/sidekick')).toBe('')
    expect(parseSidekickCommand('  /sidekick  ')).toBe('')
    expect(parseSidekickCommand('/sidekick why is the sky blue?')).toBe('why is the sky blue?')
    expect(parseSidekickCommand('  /sidekick   spaced  ')).toBe('spaced')
  })

  it('routes a question to POST /api/sessions/:id/sidekick without notifying', async () => {
    setActivePinia(createPinia())
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource
    const toastInfo = vi.spyOn(toast, 'info').mockImplementation(() => '')

    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ status: 'accepted' }), { status: 202 }),
    )
    vi.stubGlobal('fetch', fetchMock)

    try {
      const sse = useSSE(() => 'sid-ask')
      const ok = await sse.askSidekick('what is X?')

      expect(ok).toBe(true)
      expect(fetchMock).toHaveBeenCalledTimes(1)
      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
      expect(url).toContain('/sessions/sid-ask/sidekick')
      expect(JSON.parse(String(init.body))).toEqual({ question: 'what is X?' })
      expect(toastInfo).not.toHaveBeenCalled()

      // Immediate ack chip while the model call runs.
      expect(sse.sidekickNotes.value).toHaveLength(1)
      expect(sse.sidekickNotes.value[0].severity).toBe('info')
      expect(sse.sidekickNotes.value[0].text).toContain('thinking')

      // Stream auto-opened so the async answer is not dropped.
      const es = (globalThis as unknown as { __lastES: FakeEventSource }).__lastES
      expect(es.url).toContain('/sessions/sid-ask/stream')
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('delivers the answer over the auto-opened stream', async () => {
    setActivePinia(createPinia())
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource
    vi.spyOn(toast, 'info').mockImplementation(() => '')
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('{}', { status: 202 })),
    )

    try {
      const sse = useSSE(() => 'sid-live')
      await sse.askSidekick('hello?')
      expect(sse.sidekickNotes.value).toHaveLength(1) // thinking

      const es = (globalThis as unknown as { __lastES: FakeEventSource }).__lastES
      es.dispatch('sidekick', JSON.stringify({ severity: 'info', text: '42, obviously' }))

      expect(sse.sidekickNotes.value).toHaveLength(2)
      expect(sse.sidekickNotes.value[1].text).toBe('42, obviously')
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('surfaces API errors as a quiet warning chip, never a toast', async () => {
    setActivePinia(createPinia())
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource
    for (const m of ['info', 'warning', 'error', 'success'] as const) {
      vi.spyOn(toast, m).mockImplementation(() => '')
    }

    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: 'sidekick is not enabled' }), { status: 503 }),
    )
    vi.stubGlobal('fetch', fetchMock)

    try {
      const sse = useSSE(() => 'sid-err')
      const ok = await sse.askSidekick('hello?')

      expect(ok).toBe(false)
      expect(sse.sidekickNotes.value).toHaveLength(1)
      expect(sse.sidekickNotes.value[0].severity).toBe('warning')
      expect(sse.sidekickNotes.value[0].text).toContain('sidekick is not enabled')
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('shows a usage chip for a bare /sidekick and never hits the API', async () => {
    setActivePinia(createPinia())
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource

    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    try {
      const sse = useSSE(() => 'sid-usage')
      const ok = await sse.askSidekick('')

      expect(ok).toBe(false)
      expect(fetchMock).not.toHaveBeenCalled()
      expect(sse.sidekickNotes.value[0].severity).toBe('info')
      expect(sse.sidekickNotes.value[0].text).toContain('Usage:')
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
