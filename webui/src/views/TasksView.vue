<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useTaskStore, type Task, type TaskStatus, type TaskPriority } from '@/stores/task'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Plus, RefreshCw, Trash2, ArrowRight } from '@lucide/vue'

const store = useTaskStore()

// Kanban columns - only show columns that have tasks or are in the main workflow
const columns = computed(() => {
  // Always show the main 5, add extras only if tasks exist
  const mainStatuses: TaskStatus[] = ['pending', 'in_progress', 'completed', 'failed', 'cancelled']
  const extraStatuses: TaskStatus[] = ['blocked', 'skipped', 'archived']

  const active = mainStatuses.filter((s) => tasksByStatus(s).length > 0 || mainStatuses.includes(s))
  const extras = extraStatuses.filter((s) => tasksByStatus(s).length > 0)

  return [...active, ...extras]
})

function tasksByStatus(status: TaskStatus): Task[] {
  return store.tasks.filter((t) => t.status === status)
}

const columnLabels: Record<string, string> = {
  pending: 'Pending',
  in_progress: 'In Progress',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
  blocked: 'Blocked',
  skipped: 'Skipped',
  archived: 'Archived',
}

// Priority badge colors
const priorityClasses: Record<string, string> = {
  critical: 'bg-red-500/20 text-red-400 border-red-500/30',
  high: 'bg-orange-500/20 text-orange-400 border-orange-500/30',
  medium: 'bg-yellow-500/20 text-yellow-400 border-yellow-500/30',
  low: 'bg-green-500/20 text-green-400 border-green-500/30',
}

function priorityClass(priority: TaskPriority): string {
  return priorityClasses[priority] || priorityClasses.medium
}

// Status transitions - which statuses can be transitioned to from a given status
const validTransitions: Record<string, string[]> = {
  pending: ['in_progress', 'completed', 'failed', 'cancelled'],
  in_progress: ['completed', 'failed', 'cancelled'],
  blocked: ['in_progress', 'cancelled'],
  completed: ['archived'],
  failed: [],
  cancelled: [],
  skipped: [],
  archived: [],
}

function getTransitions(status: TaskStatus): TaskStatus[] {
  return (validTransitions[status] || []) as TaskStatus[]
}

function transitionLabel(status: TaskStatus): string {
  const labels: Record<string, string> = {
    in_progress: 'Start',
    completed: 'Complete',
    failed: 'Fail',
    cancelled: 'Cancel',
    archived: 'Archive',
  }
  return labels[status] || status
}

// Create task dialog
const createOpen = ref(false)
const newTitle = ref('')
const newPriority = ref<TaskPriority>('medium')
const newAssignee = ref('')

async function handleCreateTask() {
  if (!newTitle.value.trim()) return
  await store.createTask({
    title: newTitle.value.trim(),
    priority: newPriority.value,
    assignee: newAssignee.value || undefined,
  })
  createOpen.value = false
  newTitle.value = ''
  newPriority.value = 'medium'
  newAssignee.value = ''
}

// Task actions
async function handleTransition(task: Task, newStatus: TaskStatus) {
  await store.updateTask(task.id, { status: newStatus })
}

async function handleDelete(task: Task) {
  await store.deleteTask(task.id)
}

// Load on mount
onMounted(() => {
  store.fetchTasks()
})
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <h1 class="text-lg font-semibold">Task Board</h1>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="store.fetchTasks()">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': store.loading }" />
        </Button>
        <Button size="sm" @click="createOpen = true">
          <Plus class="mr-1 h-4 w-4" />
          New Task
        </Button>
      </div>
    </div>

    <!-- Kanban board -->
    <div class="flex-1 overflow-x-auto p-4">
      <div class="flex gap-4" style="min-width: max-content">
        <div
          v-for="status in columns"
          :key="status"
          class="flex w-72 shrink-0 flex-col rounded-lg border border-border bg-muted/30"
        >
          <!-- Column header -->
          <div class="flex items-center justify-between border-b border-border px-3 py-2">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium">{{ columnLabels[status] }}</span>
              <Badge variant="secondary" class="h-5 min-w-5 justify-center px-1.5 text-xs">
                {{ tasksByStatus(status).length }}
              </Badge>
            </div>
          </div>

          <!-- Column body -->
          <ScrollArea class="flex-1 px-2 py-2">
            <div class="flex flex-col gap-2">
              <template v-if="tasksByStatus(status).length > 0">
                <Card
                  v-for="task in tasksByStatus(status)"
                  :key="task.id"
                  class="group cursor-default transition-colors hover:border-border/80"
                >
                  <CardHeader class="p-3 pb-1">
                    <div class="flex items-start justify-between gap-2">
                      <CardTitle class="text-sm font-medium leading-snug">
                        {{ task.title }}
                      </CardTitle>
                      <Badge
                        :class="priorityClass(task.priority)"
                        variant="outline"
                        class="shrink-0 text-[10px] uppercase"
                      >
                        {{ task.priority }}
                      </Badge>
                    </div>
                  </CardHeader>
                  <CardContent class="p-3 pt-0">
                    <!-- Metadata -->
                    <div class="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                      <span v-if="task.assignee" class="rounded bg-muted px-1.5 py-0.5">
                        {{ task.assignee }}
                      </span>
                      <span
                        v-if="task.dependencies && task.dependencies.length > 0"
                        class="rounded bg-muted px-1.5 py-0.5"
                      >
                        deps: {{ task.dependencies.length }}
                      </span>
                    </div>

                    <!-- Action buttons -->
                    <div
                      class="mt-2 flex flex-wrap gap-1"
                      v-if="getTransitions(task.status).length > 0"
                    >
                      <Button
                        v-for="transition in getTransitions(task.status)"
                        :key="transition"
                        variant="ghost"
                        size="sm"
                        class="h-6 px-2 text-xs"
                        @click="handleTransition(task, transition as TaskStatus)"
                      >
                        <ArrowRight class="mr-1 h-3 w-3" />
                        {{ transitionLabel(transition as TaskStatus) }}
                      </Button>
                    </div>

                    <!-- Delete button (visible on hover) -->
                    <div class="mt-1 flex justify-end">
                      <Button
                        variant="ghost"
                        size="sm"
                        class="h-6 w-6 p-0 opacity-0 transition-opacity group-hover:opacity-100"
                        @click="handleDelete(task)"
                      >
                        <Trash2 class="h-3 w-3 text-muted-foreground" />
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              </template>

              <!-- Empty state -->
              <div
                v-else
                class="flex h-20 items-center justify-center text-xs text-muted-foreground"
              >
                No tasks
              </div>
            </div>
          </ScrollArea>
        </div>
      </div>
    </div>

    <!-- Create task dialog -->
    <Dialog v-model:open="createOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create Task</DialogTitle>
        </DialogHeader>
        <div class="grid gap-4 py-2">
          <div class="grid gap-2">
            <Label for="title">Title</Label>
            <Input
              id="title"
              v-model="newTitle"
              placeholder="Task title"
              @keydown.enter="handleCreateTask"
            />
          </div>
          <div class="grid gap-2">
            <Label for="priority">Priority</Label>
            <div class="flex gap-1">
              <Button
                v-for="p in (['critical', 'high', 'medium', 'low'] as TaskPriority[])"
                :key="p"
                :variant="newPriority === p ? 'default' : 'outline'"
                size="sm"
                class="h-7 flex-1 text-xs capitalize"
                @click="newPriority = p"
              >
                {{ p }}
              </Button>
            </div>
          </div>
          <div class="grid gap-2">
            <Label for="assignee">Assignee</Label>
            <Input id="assignee" v-model="newAssignee" placeholder="Optional" />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" @click="createOpen = false">Cancel</Button>
          <Button @click="handleCreateTask" :disabled="!newTitle.trim()">Create</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
