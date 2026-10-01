import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  addUserHook,
  fetchHooks,
  removeUserHooks,
  setHooksMaster,
  setUserHooksEnabled,
  shortFingerprint,
  trustHooks,
  trustLabel,
  untrustHooks,
  updateUserHook,
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
          enabled: true,
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

describe('hooks user management wrappers', () => {
  it('addUserHook POSTs the new hook', async () => {
    const list: HooksList = { enabled: true, user: [] }
    const f = stubFetch(list)
    const add = {
      event: 'PreToolUse',
      matcher: '^system_exec$',
      name: 'n',
      command: ['/bin/true'],
      timeout: 30,
      on_failure: 'allow',
    }
    await expect(addUserHook(add)).resolves.toEqual(list)
    const [, opts] = f.mock.calls[0] as [string, RequestInit & { body?: unknown }]
    expect(f.mock.calls[0][0]).toBe('/api/hooks/user/add')
    expect(JSON.parse(opts.body as string)).toEqual(add)
  })

  it('removeUserHooks POSTs fingerprints', async () => {
    const list: HooksList = { enabled: true, user: [] }
    const f = stubFetch(list)
    await expect(removeUserHooks(['sha256:abc'])).resolves.toEqual(list)
    expect(f.mock.calls[0][0]).toBe('/api/hooks/user/remove')
  })

  it('setUserHooksEnabled POSTs fingerprints with the flag', async () => {
    const list: HooksList = { enabled: true, user: [] }
    const f = stubFetch(list)
    await expect(setUserHooksEnabled(['sha256:abc'], false)).resolves.toEqual(list)
    const [, opts] = f.mock.calls[0] as [string, RequestInit & { body?: unknown }]
    expect(f.mock.calls[0][0]).toBe('/api/hooks/user/set-enabled')
    expect(JSON.parse(opts.body as string)).toEqual({ fingerprints: ['sha256:abc'], enabled: false })
  })

  it('updateUserHook POSTs the patch', async () => {
    const list: HooksList = { enabled: true, user: [] }
    const f = stubFetch(list)
    await expect(updateUserHook({ fingerprint: 'sha256:abc', name: 'n2' })).resolves.toEqual(list)
    expect(f.mock.calls[0][0]).toBe('/api/hooks/user/update')
  })

  it('setHooksMaster POSTs the flag', async () => {
    const list: HooksList = { enabled: false, user: [] }
    const f = stubFetch(list)
    await expect(setHooksMaster(false)).resolves.toEqual(list)
    expect(f.mock.calls[0][0]).toBe('/api/hooks/master')
  })
})
