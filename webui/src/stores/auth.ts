import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(localStorage.getItem('hakase_token'))
  const user = ref<{ username: string } | null>(null)

  const isLoggedIn = computed(() => !!token.value)

  async function login(username: string, password: string): Promise<boolean> {
    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      if (!res.ok) return false
      const data = await res.json()
      token.value = data.token
      user.value = { username }
      localStorage.setItem('hakase_token', data.token)
      return true
    } catch {
      return false
    }
  }

  function logout() {
    token.value = null
    user.value = null
    localStorage.removeItem('hakase_token')
  }

  return { token, user, isLoggedIn, login, logout }
})
