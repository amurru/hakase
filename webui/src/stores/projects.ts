import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiFetch, ApiError } from '@/lib/api'
import type { ProjectSummary } from './session'

// Project is the registry API shape (GET/POST /api/projects and the
// per-project sync response). error carries the bounded git stderr of a
// failed register/sync so a sync_error row explains itself.
export interface Project extends ProjectSummary {
  created_at?: string
  updated_at?: string
  error?: string
}

// ProjectStatus is the GET /api/projects/{id}/status response: the live repo
// state of a ready checkout. ahead/behind reflect the upstream after a
// best-effort fetch (the page passes fetch on); the chat header chip reads it
// with fetch off. error explains a degraded read (fetch failure, not-ready).
export interface ProjectStatus {
  id: string
  project_status: string
  branch?: string
  upstream?: string
  ahead: number
  behind: number
  staged: number
  modified: number
  untracked: number
  conflicts: number
  dirty: boolean
  error?: string
}

export interface RegisterInput {
  name: string
  url: string
  ref?: string
}

export const useProjectsStore = defineStore('projects', () => {
  const projects = ref<Project[]>([])
  const loading = ref(false)
  // error is set when the registry is unreachable or not loaded on this
  // server (e.g. 503), which the empty state must not masquerade as "none".
  const error = ref<string | null>(null)
  // Client-side guard: ids whose sync request is in flight. Register/sync are
  // synchronous server-side, so this only stops double-clicks from racing two
  // pulls/clones on the same checkout.
  const syncingIds = ref<string[]>([])
  // Live repo states keyed by project id (GET /projects/{id}/status). Fetched
  // when the Projects page opens (server refreshes behind counts) and after a
  // successful sync; the chat header reads a no-fetch snapshot for its chip.
  const statuses = ref<Record<string, ProjectStatus | undefined>>({})
  const statusLoading = ref<Record<string, boolean>>({})

  function isSyncing(id: string): boolean {
    return syncingIds.value.includes(id)
  }

  function statusOf(id: string): ProjectStatus | undefined {
    return statuses.value[id]
  }

  function isStatusLoading(id: string): boolean {
    return !!statusLoading.value[id]
  }

  async function load() {
    loading.value = true
    error.value = null
    try {
      projects.value = await apiFetch<Project[]>('/projects')
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : 'Failed to load projects'
    } finally {
      loading.value = false
    }
  }

  // loadStatus fetches one project's live repo state. fetch defaults to true
  // (the server best-effort fetches so behind counts are current); the chat
  // chip passes fetch:false. Non-200 responses (deleted / no longer ready /
  // checkout unreadable) drop any stale state and return null.
  async function loadStatus(id: string, opts: { fetch?: boolean } = {}): Promise<ProjectStatus | null> {
    if (statusLoading.value[id]) return statuses.value[id] ?? null
    statusLoading.value[id] = true
    try {
      const q = opts.fetch === false ? '?fetch=0' : ''
      const st = await apiFetch<ProjectStatus>(`/projects/${id}/status${q}`)
      statuses.value[id] = st
      return st
    } catch {
      delete statuses.value[id]
      return null
    } finally {
      delete statusLoading.value[id]
    }
  }

  // loadReadyStatuses refreshes every ready project's state (used on page
  // open). Unready rows have nothing to report and are skipped.
  async function loadReadyStatuses() {
    const ready = projects.value.filter((p) => p.status === 'ready')
    await Promise.allSettled(ready.map((p) => loadStatus(p.id)))
  }

  async function registerProject(input: RegisterInput): Promise<Project> {
    const body: Record<string, string> = { name: input.name, url: input.url }
    if (input.ref?.trim()) body.ref = input.ref.trim()
    const created = await apiFetch<Project>('/projects', { method: 'POST', body })
    // Refresh so the canonical (name-sorted) list includes the new entry with
    // its real status - a failed clone returns a sync_error row that the list
    // becomes the retry surface for.
    await load()
    return created
  }

  async function syncProject(id: string): Promise<Project | null> {
    if (isSyncing(id)) return null
    syncingIds.value.push(id)
    try {
      // A diverged/dirty pull is a 200 with status sync_error + error field,
      // so the caller decides from the returned project, not from the status.
      const updated = await apiFetch<Project>(`/projects/${id}/sync`, { method: 'POST' })
      await load()
      // A successful pull changes ahead/behind: refresh the live state so the
      // behind badge clears without a page reload.
      if (updated.status === 'ready') {
        await loadStatus(id)
      }
      return updated
    } finally {
      syncingIds.value = syncingIds.value.filter((x) => x !== id)
    }
  }

  async function removeProject(id: string): Promise<void> {
    await apiFetch(`/projects/${id}`, { method: 'DELETE' })
    projects.value = projects.value.filter((p) => p.id !== id)
    delete statuses.value[id]
  }

  return {
    projects,
    loading,
    error,
    syncingIds,
    statuses,
    statusLoading,
    isSyncing,
    statusOf,
    isStatusLoading,
    load,
    loadStatus,
    loadReadyStatuses,
    registerProject,
    syncProject,
    removeProject,
  }
})
