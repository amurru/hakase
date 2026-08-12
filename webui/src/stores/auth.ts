import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { apiGet, apiPost, setOnUnauthorized, ApiError } from '@/lib/api'
import router from '@/router'

interface LoginResponse {
  username: string
  token: string
}

interface MeResponse {
  username: string
}

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(null)
  const user = ref<{ username: string } | null>(null)
  const initialized = ref(false)

  const isLoggedIn = computed(() => !!user.value)

  async function login(username: string, password: string): Promise<{ ok: boolean; error?: string }> {
    try {
      const data = await apiPost<LoginResponse>('/login', { username, password })
      token.value = data.token
      user.value = { username: data.username }
      return { ok: true }
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 403) {
          return { ok: false, error: 'Not configured - run hakase auth set-password' }
        }
        if (err.status === 401) {
          return { ok: false, error: 'Invalid username or password' }
        }
        return { ok: false, error: (err.data as Record<string, string>)?.error || err.message }
      }
      return { ok: false, error: 'Network error - please try again' }
    }
  }

  async function logout() {
    try {
      await apiPost('/logout')
    } catch {
      // Ignore logout errors - clear state regardless
    }
    token.value = null
    user.value = null
  }

  let initPromise: Promise<void> | null = null

  async function init() {
    if (initialized.value) return
    if (!initPromise) {
      initPromise = (async () => {
        try {
          const data = await apiGet<MeResponse>('/me')
          user.value = { username: data.username }
        } catch {
          // 401 or network error - user stays null, guard handles redirect
          user.value = null
          token.value = null
        } finally {
          initialized.value = true
          initPromise = null
        }
      })()
    }
    return initPromise
  }

  // Wire up the 401 handler to redirect via router
  setOnUnauthorized(() => {
    user.value = null
    token.value = null
    router.push('/login')
  })

  return { token, user, isLoggedIn, initialized, login, logout, init }
})
