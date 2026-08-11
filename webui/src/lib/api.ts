const BASE_URL = '/api'

interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
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

export async function apiFetch<T = unknown>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { body, headers: customHeaders, ...rest } = options

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...((customHeaders as Record<string, string>) || {}),
  }

  const token = localStorage.getItem('hakase_token')
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    headers,
    body: body ? JSON.stringify(body) : undefined,
    ...rest,
  })

  if (res.status === 401) {
    localStorage.removeItem('hakase_token')
    window.location.href = '/login'
    throw new ApiError(401, 'Unauthorized')
  }

  const data = await res.json().catch(() => null)

  if (!res.ok) {
    throw new ApiError(res.status, `API error: ${res.statusText}`, data)
  }

  return data as T
}
