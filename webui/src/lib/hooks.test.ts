import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  fetchHooks,
  shortFingerprint,
  trustHooks,
  trustLabel,
  untrustHooks,
  type HooksList,
} from './hooks'

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

describe('hooks api wrappers', () => {
  it('fetchHooks GETs /hooks', async () => {
    const list: HooksList = {
      enabled: true,
      user: [
        {
          event: 'PreToolUse',
          matcher: '^system_exec$',
          name: 'no-rm-rf',
          command: ['/home/you/.hakase/hooks/no-rm-rf.sh'],
          timeout: 30,
          on_failure: 'allow',
          fingerprint: 'sha256:abc123',
          layer: 'user',
          trusted: true,
        },
      ],
    }
    const f = stubFetch(list)
    await expect(fetchHooks()).resolves.toEqual(list)
    expect(f).toHaveBeenCalledWith('/api/hooks', expect.objectContaining({ headers: expect.anything() }))
  })

  it('fetchHooks passes session scope as a query param', async () => {
    const list: HooksList = { enabled: true, user: [] }
    const f = stubFetch(list)
    await fetchHooks('sess_1')
    expect(f).toHaveBeenCalledWith(
      '/api/hooks?session_id=sess_1',
      expect.objectContaining({ headers: expect.anything() }),
    )
  })

  it('trustHooks POSTs fingerprints with session scope', async () => {
    const f = stubFetch({ changed: ['sha256:abc'] })
    await expect(trustHooks(['sha256:abc'], 'sess_1')).resolves.toEqual({ changed: ['sha256:abc'] })
    const [, opts] = f.mock.calls[0] as [string, RequestInit & { body?: unknown }]
    expect(f.mock.calls[0][0]).toBe('/api/hooks/trust')
    expect(opts.method).toBe('POST')
    expect(JSON.parse(opts.body as string)).toEqual({
      session_id: 'sess_1',
      fingerprints: ['sha256:abc'],
    })
  })

  it('untrustHooks POSTs fingerprints', async () => {
    const f = stubFetch({ changed: ['sha256:abc'] })
    await expect(untrustHooks(['sha256:abc'])).resolves.toEqual({ changed: ['sha256:abc'] })
    const [, opts] = f.mock.calls[0] as [string, RequestInit & { body?: unknown }]
    expect(f.mock.calls[0][0]).toBe('/api/hooks/untrust')
    expect(JSON.parse(opts.body as string)).toEqual({ fingerprints: ['sha256:abc'] })
  })

  it('shortFingerprint truncates to a readable prefix', () => {
    expect(shortFingerprint('sha256:' + 'a'.repeat(64))).toBe(`sha256:${'a'.repeat(12)}`)
  })

  it('trustLabel distinguishes own config from trust state', () => {
    expect(trustLabel({ layer: 'user', trusted: true })).toBe('own config')
    expect(trustLabel({ layer: 'project', trusted: true })).toBe('trusted')
    expect(trustLabel({ layer: 'project', trusted: false })).toBe('untrusted')
  })
})
