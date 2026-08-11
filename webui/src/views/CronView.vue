<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  RefreshCw,
  Play,
  Pause,
  PlayCircle,
  Clock,
  AlertCircle,
  CalendarClock,
} from '@lucide/vue'

interface CronJob {
  id: string
  name: string
  prompt?: string
  schedule: string
  skills?: string[]
  repeat?: number
  state: string
  enabled: boolean
  native?: string
  next_run_at?: string
  last_run_at?: string
  last_status?: string
  run_count: number
  created_at: string
  updated_at: string
}

const jobs = ref<CronJob[]>([])
const loading = ref(false)
const error = ref('')
const actionPending = ref<string | null>(null)

async function loadJobs() {
  loading.value = true
  error.value = ''
  try {
    jobs.value = await apiFetch<CronJob[]>('/cron/jobs')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load cron jobs'
  } finally {
    loading.value = false
  }
}

async function pauseJob(id: string) {
  actionPending.value = id
  try {
    await apiFetch(`/cron/jobs/${encodeURIComponent(id)}/pause`, { method: 'POST' })
    await loadJobs()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to pause job'
  } finally {
    actionPending.value = null
  }
}

async function resumeJob(id: string) {
  actionPending.value = id
  try {
    await apiFetch(`/cron/jobs/${encodeURIComponent(id)}/resume`, { method: 'POST' })
    await loadJobs()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to resume job'
  } finally {
    actionPending.value = null
  }
}

async function runJob(id: string) {
  actionPending.value = id
  try {
    await apiFetch(`/cron/jobs/${encodeURIComponent(id)}/run`, { method: 'POST' })
    await loadJobs()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to trigger job'
  } finally {
    actionPending.value = null
  }
}

function stateColor(state: string): string {
  switch (state) {
    case 'scheduled':
      return 'bg-green-500/20 text-green-400 border-green-500/30'
    case 'paused':
      return 'bg-yellow-500/20 text-yellow-400 border-yellow-500/30'
    case 'running':
      return 'bg-blue-500/20 text-blue-400 border-blue-500/30'
    case 'completed':
      return 'bg-gray-500/20 text-gray-400 border-gray-500/30'
    default:
      return 'bg-muted text-muted-foreground border-border'
  }
}

function formatTime(iso: string | undefined): string {
  if (!iso) return '-'
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return '-'
    return d.toLocaleString()
  } catch {
    return '-'
  }
}

function jobLabel(job: CronJob): string {
  return job.name || job.id.slice(0, 8)
}

onMounted(loadJobs)
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <h1 class="text-lg font-semibold">Cron Jobs</h1>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="loadJobs">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        </Button>
      </div>
    </div>

    <!-- Content -->
    <div class="flex-1 overflow-auto p-4">
      <!-- Loading -->
      <div v-if="loading && jobs.length === 0" class="flex h-40 items-center justify-center text-sm text-muted-foreground">
        Loading cron jobs...
      </div>

      <!-- Error -->
      <div v-else-if="error && jobs.length === 0" class="flex h-40 flex-col items-center justify-center gap-2">
        <div class="flex items-center gap-2 text-sm text-destructive">
          <AlertCircle class="h-4 w-4 shrink-0" />
          <span>{{ error }}</span>
        </div>
        <Button variant="ghost" size="sm" @click="loadJobs">
          Retry
        </Button>
      </div>

      <!-- Empty state -->
      <div v-else-if="jobs.length === 0" class="flex h-40 flex-col items-center justify-center text-muted-foreground">
        <CalendarClock class="mb-2 h-8 w-8 opacity-50" />
        <p class="text-sm">No cron jobs configured</p>
        <p class="mt-1 text-xs">Use the hakase cronjob tool or CLI to create scheduled tasks</p>
      </div>

      <!-- Job list -->
      <ScrollArea v-else class="h-full">
        <div class="flex flex-col gap-3">
          <div
            v-for="job in jobs"
            :key="job.id"
            class="group"
          >
            <Card class="transition-colors hover:border-border/80">
              <CardContent class="p-4">
                <div class="flex items-start justify-between gap-4">
                  <!-- Job info -->
                  <div class="flex min-w-0 flex-1 flex-col gap-2">
                    <div class="flex items-center gap-2">
                      <!-- Name -->
                      <span class="truncate text-sm font-medium">{{ jobLabel(job) }}</span>
                      <!-- State badge -->
                      <Badge
                        :class="stateColor(job.state)"
                        variant="outline"
                        class="shrink-0 text-[10px] uppercase"
                      >
                        {{ job.state }}
                      </Badge>
                      <!-- Native badge -->
                      <Badge
                        v-if="job.native"
                        variant="outline"
                        class="shrink-0 bg-purple-500/20 text-purple-400 border-purple-500/30 text-[10px] uppercase"
                      >
                        {{ job.native }}
                      </Badge>
                    </div>

                    <!-- Schedule + times -->
                    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                      <span class="flex items-center gap-1 font-mono">
                        <Clock class="h-3 w-3 shrink-0" />
                        {{ job.schedule }}
                      </span>
                      <span v-if="job.next_run_at">
                        Next: {{ formatTime(job.next_run_at) }}
                      </span>
                      <span v-if="job.last_run_at">
                        Last: {{ formatTime(job.last_run_at) }}
                        <span v-if="job.last_status" class="ml-1">({{ job.last_status }})</span>
                      </span>
                      <span v-if="job.run_count > 0">
                        Runs: {{ job.run_count }}
                      </span>
                    </div>

                    <!-- Skills -->
                    <div v-if="job.skills && job.skills.length > 0" class="flex flex-wrap gap-1">
                      <Badge
                        v-for="skill in job.skills"
                        :key="skill"
                        variant="secondary"
                        class="text-[10px]"
                      >
                        {{ skill }}
                      </Badge>
                    </div>

                    <!-- Prompt preview -->
                    <p v-if="job.prompt" class="line-clamp-1 text-xs text-muted-foreground">
                      {{ job.prompt }}
                    </p>
                  </div>

                  <!-- Actions -->
                  <div class="flex shrink-0 items-center gap-1">
                    <Button
                      v-if="job.state === 'scheduled' || job.state === 'running'"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2"
                      :disabled="actionPending === job.id"
                      @click="pauseJob(job.id)"
                    >
                      <Pause class="mr-1 h-3 w-3" />
                      Pause
                    </Button>
                    <Button
                      v-else-if="job.state === 'paused'"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2"
                      :disabled="actionPending === job.id"
                      @click="resumeJob(job.id)"
                    >
                      <Play class="mr-1 h-3 w-3" />
                      Resume
                    </Button>
                    <Button
                      v-if="job.state !== 'completed'"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2"
                      :disabled="actionPending === job.id"
                      @click="runJob(job.id)"
                    >
                      <PlayCircle class="mr-1 h-3 w-3" />
                      Run
                    </Button>
                  </div>
                </div>
              </CardContent>
            </Card>
          </div>
        </div>
      </ScrollArea>

      <!-- Inline error (shown when jobs are loaded but an action failed) -->
      <div
        v-if="error && jobs.length > 0"
        class="mt-3 flex items-center gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
      >
        <AlertCircle class="h-3.5 w-3.5 shrink-0" />
        <span>{{ error }}</span>
        <Button
          variant="ghost"
          size="sm"
          class="ml-auto h-6 px-2"
          @click="error = ''"
        >
          Dismiss
        </Button>
      </div>
    </div>
  </div>
</template>
