import { apiFetch, apiPost } from '@/lib/api'

// Types and wrappers for the /api/hooks endpoints (handlers/hooks.go):
// user-layer inspection plus trust-gated project hooks (docs/hooks/).

export interface HookSnapshot {
  event: string
  matcher: string
  name: string
  command: string[]
  timeout: number
  on_failure: string
  fingerprint: string
  layer: 'user' | 'project'
  trusted: boolean
  enabled: boolean
}

export interface HooksProject {
  root: string
  file: string
  hooks: HookSnapshot[]
}

export interface HooksList {
  enabled: boolean
  user: HookSnapshot[]
  project?: HooksProject | null
}

export function fetchHooks(sessionId?: string): Promise<HooksList> {
  const q = sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : ''
  return apiFetch<HooksList>(`/hooks${q}`)
}

export function trustHooks(fingerprints: string[], sessionId?: string): Promise<{ changed: string[] }> {
  return apiPost<{ changed: string[] }>('/hooks/trust', {
    session_id: sessionId ?? '',
    fingerprints,
  })
}

export function untrustHooks(fingerprints: string[]): Promise<{ changed: string[] }> {
  return apiPost<{ changed: string[] }>('/hooks/untrust', { fingerprints })
}

export interface UserHookAdd {
  event: string
  matcher: string
  name: string
  command: string[]
  timeout: number
  on_failure: string
}

export interface UserHookUpdate {
  fingerprint: string
  matcher?: string
  name?: string
  command?: string[]
  timeout?: number
  on_failure?: string
  enabled?: boolean
}

export function addUserHook(add: UserHookAdd): Promise<HooksList> {
  return apiPost<HooksList>('/hooks/user/add', add)
}

export function removeUserHooks(fingerprints: string[]): Promise<HooksList> {
  return apiPost<HooksList>('/hooks/user/remove', { fingerprints })
}

export function setUserHooksEnabled(fingerprints: string[], enabled: boolean): Promise<HooksList> {
  return apiPost<HooksList>('/hooks/user/set-enabled', { fingerprints, enabled })
}

export function updateUserHook(update: UserHookUpdate): Promise<HooksList> {
  return apiPost<HooksList>('/hooks/user/update', update)
}

export function setHooksMaster(enabled: boolean): Promise<HooksList> {
  return apiPost<HooksList>('/hooks/master', { enabled })
}

export function shortFingerprint(fp: string): string {
  const hex = fp.startsWith('sha256:') ? fp.slice('sha256:'.length) : fp
  return `sha256:${hex.slice(0, 12)}`
}

export function trustLabel(hook: Pick<HookSnapshot, 'layer' | 'trusted'>): string {
  if (hook.layer !== 'project') return 'own config'
  return hook.trusted ? 'trusted' : 'untrusted'
}
