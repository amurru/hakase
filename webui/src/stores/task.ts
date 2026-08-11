import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiFetch } from '@/lib/api'

export interface Task {
  id: string
  version: number
  title: string
  description?: string
  status: TaskStatus
  priority: TaskPriority
  owner?: string
  assignee?: string
  dependencies?: string[]
  blocked_by?: string[]
  created_at: string
  updated_at: string
  started_at?: string
  completed_at?: string
  attempts: number
  max_attempts: number
  last_error?: string
  parent_id?: string
  tags?: string[]
  metadata?: Record<string, unknown>
}

export type TaskStatus =
  | 'pending'
  | 'in_progress'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'skipped'
  | 'blocked'
  | 'archived'

export type TaskPriority = 'critical' | 'high' | 'medium' | 'low'

export interface CreateTaskPayload {
  title: string
  description?: string
  priority?: TaskPriority
  assignee?: string
  dependencies?: string[]
  tags?: string[]
  parent_id?: string
}

export interface UpdateTaskPayload {
  title?: string
  description?: string
  status?: TaskStatus
  priority?: TaskPriority
  assignee?: string
  error?: string
}

export const useTaskStore = defineStore('tasks', () => {
  const tasks = ref<Task[]>([])
  const loading = ref(false)

  async function fetchTasks() {
    loading.value = true
    try {
      const data = await apiFetch<Task[]>('/tasks')
      tasks.value = data
    } finally {
      loading.value = false
    }
  }

  async function createTask(payload: CreateTaskPayload): Promise<Task> {
    const task = await apiFetch<Task>('/tasks', {
      method: 'POST',
      body: payload,
    })
    tasks.value.push(task)
    return task
  }

  async function updateTask(
    id: string,
    payload: UpdateTaskPayload,
  ): Promise<Task> {
    const task = await apiFetch<Task>(`/tasks/${id}`, {
      method: 'PATCH',
      body: payload,
    })
    const idx = tasks.value.findIndex((t) => t.id === id)
    if (idx !== -1) {
      tasks.value[idx] = task
    }
    return task
  }

  async function deleteTask(id: string) {
    await apiFetch(`/tasks/${id}`, { method: 'DELETE' })
    tasks.value = tasks.value.filter((t) => t.id !== id)
  }

  return { tasks, loading, fetchTasks, createTask, updateTask, deleteTask }
})
