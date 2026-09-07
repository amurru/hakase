import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  fetchChannelsStatus,
  requestPairingCode,
  revokeTelegramUser,
  saveTelegramToken,
  clearTelegramToken,
  setTelegramEnabled,
  formatCountdown,
  secondsUntil,
  shortId,
  type ChannelsStatus,
} from './channels'

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

describe('channels api wrappers', () => {
  it('fetchChannelsStatus GETs /channels', async () => {
    const status: ChannelsStatus = {
      telegram: { enabled: true, running: false, paired_users: [], chats: [] },
    }
    const f = stubFetch(status)
    await expect(fetchChannelsStatus()).resolves.toEqual(status)
    expect(f).toHaveBeenCalledWith('/api/channels', expect.objectContaining({ headers: expect.anything() }))
  })

  it('requestPairingCode POSTs and returns the code', async () => {
    stubFetch({ code: '123456', expires_at: '2026-01-01T00:00:00Z', ttl_seconds: 900 })
    await expect(requestPairingCode()).resolves.toMatchObject({ code: '123456' })
  })

  it('revokeTelegramUser posts user id with telegram channel', async () => {
    const f = stubFetch({ status: 'revoked' })
    await revokeTelegramUser(42)
    const [path, opts] = f.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/channels/revoke')
    expect(opts.method).toBe('POST')
    expect(JSON.parse(String(opts.body))).toEqual({ user_id: 42, channel: 'telegram' })
  })

  it('token controls use the write-only config control keys', async () => {
    const f = stubFetch({ status: 'saved' })
    await saveTelegramToken('tok')
    await clearTelegramToken()
    await setTelegramEnabled(true)

    const bodies = f.mock.calls.map(([, opts]) => JSON.parse(String((opts as RequestInit).body)))
    expect(bodies[0]).toEqual({ telegram_bot_token: 'tok' })
    expect(bodies[1]).toEqual({ clear_telegram_bot_token: true })
    expect(bodies[2]).toEqual({ channels: { telegram: { enabled: true } } })
  })

  it('surfaces API errors from failed requests', async () => {
    stubFetch({ error: 'nope' }, 403)
    await expect(requestPairingCode()).rejects.toThrow('nope')
  })
})

describe('formatting helpers', () => {
  it('formatCountdown renders m:ss and expired', () => {
    expect(formatCountdown(null)).toBe('-')
    expect(formatCountdown(0)).toBe('expired')
    expect(formatCountdown(-5)).toBe('expired')
    expect(formatCountdown(65)).toBe('1:05')
    expect(formatCountdown(900)).toBe('15:00')
  })

  it('secondsUntil clamps and rejects bad input', () => {
    expect(secondsUntil(undefined)).toBeNull()
    expect(secondsUntil('not-a-date')).toBeNull()
    expect(secondsUntil(new Date(Date.now() - 60_000).toISOString())).toBe(0)
    expect(secondsUntil(new Date(Date.now() + 5_000).toISOString())).toBeGreaterThan(0)
  })

  it('shortId truncates long ids', () => {
    expect(shortId(undefined)).toBe('-')
    expect(shortId('short')).toBe('short')
    expect(shortId('sess_0123456789abcdef')).toBe('sess_012345678…')
  })
})
