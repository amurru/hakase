const BASE_URL = '/api'

interface RequestOptions extends Omit<RequestInit, 'body' | 'method'> {
  body?: unknown
  method?: string
}

export class ApiError extends Error {
  status: number
  data: unknown

  constructor(status: number, message: string, data?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.data = data
  }
}

let onUnauthorized: (() => void) | null = null

export function setOnUnauthorized(handler: () => void) {
  onUnauthorized = handler
}

export async function apiFetch<T = unknown>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { body, headers: customHeaders, ...rest } = options

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...((customHeaders as Record<string, string>) || {}),
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    headers,
    body: body ? JSON.stringify(body) : undefined,
    ...rest,
  })

  if (res.status === 401) {
    if (onUnauthorized) {
      onUnauthorized()
    } else {
      window.location.href = '/login'
    }
    throw new ApiError(401, 'Unauthorized')
  }

  const data = await res.json().catch(() => null)

  if (!res.ok) {
    const message = (data as Record<string, string>)?.error || `API error: ${res.statusText}`
    throw new ApiError(res.status, message, data)
  }

  return data as T
}

export function apiGet<T = unknown>(path: string): Promise<T> {
  return apiFetch<T>(path, { method: 'GET' })
}

export function apiPost<T = unknown>(path: string, body?: unknown): Promise<T> {
  return apiFetch<T>(path, { method: 'POST', body })
}

export interface MediaStatusResponse {
  image_provider: string
  video_provider: string
  audio_provider: string
  resolved_image: string
  resolved_video: string
  resolved_audio: string
  capabilities: Record<string, Record<string, boolean>>
  output_dir: string
}

export function getMediaStatus(): Promise<MediaStatusResponse> {
  return apiGet<MediaStatusResponse>('/media/status')
}

export function getMediaManifest(): Promise<unknown[]> {
  return apiGet<unknown[]>('/media/manifest')
}
