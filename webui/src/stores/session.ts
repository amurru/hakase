import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiFetch } from '@/lib/api'

export interface SessionSummary {
  id: string
  title: string
  project_id?: string
  project_name?: string
  updated_at: string
  message_count: number
}

export interface SessionDetail {
  id: string
  title: string
  project_id?: string
  project_name?: string
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

export interface ProjectSummary {
  id: string
  name: string
  source_url: string
  ref?: string
  checkout?: string
  status: string
}

export interface ActiveSession {
  active: boolean
  session_id?: string
}

export const useSessionStore = defineStore('session', () => {
  const sessions = ref<SessionSummary[]>([])
  const archivedSessions = ref<SessionSummary[]>([])
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

  async function fetchArchivedSessions() {
    try {
      archivedSessions.value = await apiFetch<SessionSummary[]>('/sessions/archived')
    } catch {
      archivedSessions.value = []
    }
  }

  async function createSession(title: string, projectId?: string): Promise<SessionSummary | null> {
    try {
      const body: Record<string, string> = { title }
      if (projectId) body.project_id = projectId
      const created = await apiFetch<SessionSummary>('/sessions', {
        method: 'POST',
        body,
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
      await Promise.all([fetchSessions(), fetchArchivedSessions(), fetchActiveSession()])
      return true
    } catch {
      return false
    }
  }

  async function unarchiveSession(id: string): Promise<boolean> {
    try {
      await apiFetch(`/sessions/${id}/unarchive`, { method: 'POST' })
      await Promise.all([fetchSessions(), fetchArchivedSessions()])
      return true
    } catch {
      return false
    }
  }

  async function renameSession(id: string, title: string): Promise<boolean> {
    try {
      await apiFetch(`/sessions/${id}/rename`, {
        method: 'POST',
        body: { title },
      })
      await fetchSessions()
      await fetchArchivedSessions()
      return true
    } catch {
      return false
    }
  }

  async function deleteSession(id: string): Promise<boolean> {
    try {
      await apiFetch(`/sessions/${id}`, { method: 'DELETE' })
      await Promise.all([fetchSessions(), fetchArchivedSessions(), fetchActiveSession()])
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

  // fetchProjects lists registered remote projects from the registry API
  // (ready ones are offered when creating a project-bound session).
  async function fetchProjects(): Promise<ProjectSummary[]> {
    try {
      return await apiFetch<ProjectSummary[]>('/projects')
    } catch {
      return []
    }
  }

  return {
    sessions,
    archivedSessions,
    activeSession,
    loading,
    fetchSessions,
    fetchArchivedSessions,
    createSession,
    switchSession,
    archiveSession,
    unarchiveSession,
    renameSession,
    deleteSession,
    fetchActiveSession,
    fetchProjects,
  }
})
