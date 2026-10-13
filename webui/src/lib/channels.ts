import { apiFetch } from '@/lib/api'

// Types for the /api/channels management endpoints (handlers/channels.go).

export interface PairedChannelUser {
  channel: string
  user_id: number
  username?: string
  paired_at: string
}

export interface ChannelChat {
  chat_id: number
  session_id?: string
  notify: boolean
}

export interface ChannelStatus {
  enabled: boolean
  running: boolean
  paired_users: PairedChannelUser[]
  pending_pairing?: { expires_at: string }
  chats: ChannelChat[]
}

// TelegramChannelStatus is kept as an alias for existing imports.
export type TelegramChannelStatus = ChannelStatus

export type ChannelsStatus = Record<string, ChannelStatus>

export interface PairingCodeResponse {
  code: string
  expires_at: string
  ttl_seconds: number
}

export function fetchChannelsStatus(): Promise<ChannelsStatus> {
  return apiFetch<ChannelsStatus>('/channels')
}

export function requestPairingCode(): Promise<PairingCodeResponse> {
  return apiFetch<PairingCodeResponse>('/channels/pairing-code', { method: 'POST' })
}

export function revokeChannelUser(userId: number, channel: string): Promise<void> {
  return apiFetch('/channels/revoke', {
    method: 'POST',
    body: { user_id: userId, channel },
  }) as Promise<void>
}

export function revokeTelegramUser(userId: number): Promise<void> {
  return revokeChannelUser(userId, 'telegram')
}

export function revokeDiscordUser(userId: number): Promise<void> {
  return revokeChannelUser(userId, 'discord')
}

// Token and enablement go through the config API: the token is write-only
// (telegram_bot_token / clear_telegram_bot_token control keys) and never
// comes back from GET; enablement is a nested config edit. Both need a
// server restart to take effect on the running channel service.
export async function saveTelegramToken(token: string): Promise<void> {
  await apiFetch('/config', { method: 'PUT', body: { telegram_bot_token: token } })
}

export async function clearTelegramToken(): Promise<void> {
  await apiFetch('/config', { method: 'PUT', body: { clear_telegram_bot_token: true } })
}

export async function setTelegramEnabled(enabled: boolean): Promise<void> {
  await apiFetch('/config', {
    method: 'PUT',
    body: { channels: { telegram: { enabled } } },
  })
}

export async function saveDiscordToken(token: string): Promise<void> {
  await apiFetch('/config', { method: 'PUT', body: { discord_bot_token: token } })
}

export async function clearDiscordToken(): Promise<void> {
  await apiFetch('/config', { method: 'PUT', body: { clear_discord_bot_token: true } })
}

export async function setDiscordEnabled(enabled: boolean): Promise<void> {
  await apiFetch('/config', {
    method: 'PUT',
    body: { channels: { discord: { enabled } } },
  })
}

export interface ConfigSecretFlags {
  has_telegram_bot_token?: boolean
  has_telegram_pairing_code?: boolean
  has_discord_bot_token?: boolean
  has_discord_pairing_code?: boolean
}

export function fetchSecretFlags(): Promise<ConfigSecretFlags> {
  return apiFetch<ConfigSecretFlags>('/config')
}

// secondsUntil returns whole seconds left until an ISO timestamp (clamped at
// zero), or null when the timestamp is missing/unparsable.
export function secondsUntil(iso: string | undefined): number | null {
  if (!iso) return null
  const t = Date.parse(iso)
  if (isNaN(t)) return null
  return Math.max(0, Math.floor((t - Date.now()) / 1000))
}

// formatCountdown renders remaining seconds as m:ss (or "expired").
export function formatCountdown(seconds: number | null): string {
  if (seconds === null) return '-'
  if (seconds <= 0) return 'expired'
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${m}:${String(s).padStart(2, '0')}`
}

// shortId truncates long identifiers for table cells.
export function shortId(id: string | undefined): string {
  if (!id) return '-'
  return id.length > 14 ? id.slice(0, 14) + '…' : id
}
