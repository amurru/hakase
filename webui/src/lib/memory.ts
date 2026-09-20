import { apiFetch } from '@/lib/api'

// Types and wrappers for the /api/memory endpoints (handlers/memory.go):
// agent-written auto-memory notes (docs/auto-memory/spec.md).

export type MemoryCategory = 'user' | 'feedback' | 'project' | 'lesson'

export const MEMORY_CATEGORIES: MemoryCategory[] = ['user', 'feedback', 'project', 'lesson']

export interface MemoryNote {
  id: string
  category: MemoryCategory
  content: string
  project?: string
  created_at: string
  updated_at: string
}

export interface MemoryList {
  notes: MemoryNote[]
  store_path: string
}

export function fetchMemory(): Promise<MemoryList> {
  return apiFetch<MemoryList>('/memory')
}

export function forgetMemoryNote(id: string): Promise<{ forgotten: boolean }> {
  return apiFetch(`/memory/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function scopeLabel(note: Pick<MemoryNote, 'project'>): string {
  return note.project || 'global'
}

export function groupByCategory(notes: MemoryNote[]): Record<MemoryCategory, MemoryNote[]> {
  const groups: Record<MemoryCategory, MemoryNote[]> = {
    user: [],
    feedback: [],
    project: [],
    lesson: [],
  }
  for (const note of notes) {
    if (MEMORY_CATEGORIES.includes(note.category)) {
      groups[note.category].push(note)
    }
  }
  return groups
}
