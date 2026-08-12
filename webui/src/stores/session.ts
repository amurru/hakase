import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiFetch } from '@/lib/api'

export interface SessionSummary {
  id: string
  title: string
  updated_at: string
  message_count: number
}

export interface SessionDetail {
  id: string
  title: string
  created_at: string
  updated_at: string
  archived: boolean
  messages: Array<{
    role: string
    content: string
    thinking: string
    timestamp: string
    tokens: number
    sequence: number
    kind: string
  }>
}

export interface ActiveSession {
  active: boolean
  session_id?: string
}

export const useSessionStore = defineStore('session', () => {
  const sessions = ref<SessionSummary[]>([])
  const activeSession = ref<ActiveSession | null>(null)
  const loading = ref(false)

  async function fetchSessions() {
    loading.value = true
    try {
      sessions.value = await apiFetch<SessionSummary[]>('/sessions')
    } finally {
      loading.value = false
    }
  }

  async function createSession(title: string): Promise<SessionSummary | null> {
    try {
      const created = await apiFetch<SessionSummary>('/sessions', {
        method: 'POST',
        body: { title },
      })
      await fetchSessions()
      return created
    } catch {
      return null
    }
  }

  async function switchSession(id: string): Promise<boolean> {
    try {
      await apiFetch(`/sessions/${id}/activate`, { method: 'POST' })
      await fetchActiveSession()
      return true
    } catch {
      return false
    }
  }

  async function archiveSession(id: string): Promise<boolean> {
    try {
      await apiFetch(`/sessions/${id}/archive`, { method: 'POST' })
      await fetchSessions()
      return true
    } catch {
      return false
    }
  }

  async function deleteSession(id: string): Promise<boolean> {
    try {
      await apiFetch(`/sessions/${id}`, { method: 'DELETE' })
      await fetchSessions()
      return true
    } catch {
      return false
    }
  }

  async function fetchActiveSession() {
    try {
      activeSession.value = await apiFetch<ActiveSession>('/sessions/active')
    } catch {
      activeSession.value = { active: false }
    }
  }

  return {
    sessions,
    activeSession,
    loading,
    fetchSessions,
    createSession,
    switchSession,
    archiveSession,
    deleteSession,
    fetchActiveSession,
  }
})
