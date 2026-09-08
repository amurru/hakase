<script setup lang="ts">
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { Bot, Globe, Terminal, Sparkles, Loader2 } from '@lucide/vue'
import { formatDuration, type CanvasNode } from '@/lib/canvas'

const props = defineProps<{
  data: { node: CanvasNode }
  selected?: boolean
}>()

const node = computed(() => props.data.node)

const icon = computed(() => {
  switch (node.value.agent) {
    case 'web_researcher':
      return Globe
    case 'code_interpreter':
      return Terminal
    case 'general_purpose':
      return Bot
    default:
      return Sparkles
  }
})

const statusLabel = computed(() => {
  switch (node.value.status) {
    case 'running':
      return 'running'
    case 'failed':
      return 'failed'
    case 'timed_out':
      return 'timed out'
    default:
      return 'done'
  }
})

const statusClass = computed(() => {
  switch (node.value.status) {
    case 'completed':
      return 'text-emerald-500'
    case 'failed':
    case 'timed_out':
      return 'text-red-400'
    default:
      return 'text-primary'
  }
})

const duration = computed(() => formatDuration(node.value.durationMs))
</script>

<template>
  <div
    class="w-[208px] rounded-lg border bg-card text-card-foreground shadow-sm transition-shadow"
    :class="selected ? 'border-primary ring-2 ring-primary/40' : ''"
  >
    <Handle v-if="node.parentId" type="target" :position="Position.Top" />
    <div class="flex items-center gap-2 px-3 pt-2">
      <component :is="icon" class="h-4 w-4 shrink-0 text-muted-foreground" />
      <span class="truncate text-sm font-medium">{{ node.label }}</span>
      <Loader2 v-if="node.status === 'running'" class="ml-auto h-3.5 w-3.5 shrink-0 animate-spin text-primary" />
      <span
        v-else
        class="ml-auto h-2 w-2 shrink-0 rounded-full"
        :class="node.status === 'completed' ? 'bg-emerald-500' : 'bg-red-400'"
      />
    </div>
    <p v-if="node.goal || node.summary" class="line-clamp-2 px-3 pt-1 text-xs text-muted-foreground">
      {{ node.status === 'running' ? node.goal : node.summary || node.goal }}
    </p>
    <div class="mt-2 flex items-center justify-between border-t px-3 py-1 text-[10px] text-muted-foreground">
      <span :class="statusClass">{{ statusLabel }}</span>
      <span v-if="duration" class="font-mono">{{ duration }}</span>
    </div>
    <Handle type="source" :position="Position.Bottom" />
  </div>
</template>
