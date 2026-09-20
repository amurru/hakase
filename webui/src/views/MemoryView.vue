<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  MEMORY_CATEGORIES,
  fetchMemory,
  forgetMemoryNote,
  groupByCategory,
  scopeLabel,
  type MemoryCategory,
  type MemoryNote,
} from '@/lib/memory'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Brain, RefreshCw, Trash2, Loader2 } from '@lucide/vue'

const notes = ref<MemoryNote[]>([])
const storePath = ref('')
const loading = ref(false)
const error = ref('')
const filter = ref<MemoryCategory | 'all'>('all')
const forgetting = ref<string | null>(null)

const visibleNotes = computed(() =>
  filter.value === 'all' ? notes.value : notes.value.filter((n) => n.category === filter.value),
)

const grouped = computed(() => groupByCategory(visibleNotes.value))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const list = await fetchMemory()
    notes.value = list.notes
    storePath.value = list.store_path
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load memory notes'
  } finally {
    loading.value = false
  }
}

async function forget(note: MemoryNote) {
  forgetting.value = note.id
  try {
    await forgetMemoryNote(note.id)
    notes.value = notes.value.filter((n) => n.id !== note.id)
    toast.success('Note forgotten')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to forget note')
  } finally {
    forgetting.value = null
  }
}

function setFilter(category: MemoryCategory | 'all') {
  filter.value = category
}

onMounted(load)
</script>

<template>
  <div class="space-y-6 p-6">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">
        <Brain class="h-6 w-6" />
        <h1 class="text-2xl font-semibold">Memory</h1>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw class="mr-1 h-4 w-4" :class="{ 'animate-spin': loading }" />
        Refresh
      </Button>
    </div>

    <p class="text-sm text-muted-foreground">
      Notes the agent saves about you and your projects across sessions, injected at session
      start. Prune stale notes to keep memory useful — the agent sees ids and can also be asked to
      forget things.
    </p>

    <div class="flex flex-wrap items-center gap-2">
      <Button
        v-for="category in ['all', ...MEMORY_CATEGORIES] as const"
        :key="category"
        :variant="filter === category ? 'default' : 'outline'"
        size="sm"
        @click="setFilter(category)"
      >
        {{ category }}
      </Button>
    </div>

    <div v-if="error" class="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-300">
      {{ error }}
    </div>

    <div v-if="!loading && !error && visibleNotes.length === 0" class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground">
      No memory notes{{ filter === 'all' ? ' yet' : ` in "${filter}"` }}. Ask the agent to
      <span class="font-mono">remember</span> something and it will appear here
      ({{ storePath || '~/.hakase/memory/notes.json' }}).
    </div>

    <div v-for="category in MEMORY_CATEGORIES" :key="category" class="space-y-2">
      <template v-if="grouped[category].length > 0">
        <h2 class="text-sm font-medium text-muted-foreground">{{ category }}</h2>
        <Card v-for="note in grouped[category]" :key="note.id">
          <CardContent class="flex items-start justify-between gap-4 py-4">
            <div class="min-w-0 space-y-1">
              <p class="break-words text-sm">{{ note.content }}</p>
              <div class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge variant="secondary" class="font-mono">{{ note.id }}</Badge>
                <span>{{ scopeLabel(note) }}</span>
                <span>{{ new Date(note.updated_at).toLocaleString() }}</span>
              </div>
            </div>
            <Button
              variant="ghost"
              size="icon"
              :disabled="forgetting === note.id"
              :aria-label="`Forget note ${note.id}`"
              @click="forget(note)"
            >
              <Loader2 v-if="forgetting === note.id" class="h-4 w-4 animate-spin" />
              <Trash2 v-else class="h-4 w-4" />
            </Button>
          </CardContent>
        </Card>
      </template>
    </div>
  </div>
</template>
