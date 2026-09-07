<script setup lang="ts">
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { Loader2, Wrench } from '@lucide/vue'
import { formatDuration, type CanvasNode } from '@/lib/canvas'

const props = defineProps<{
  data: { node: CanvasNode }
  selected?: boolean
}>()

const node = computed(() => props.data.node)
const duration = computed(() => formatDuration(node.value.durationMs))
</script>

<template>
  <div
    class="w-[176px] rounded-md border bg-muted/40 text-card-foreground shadow-sm"
    :class="selected ? 'border-primary ring-2 ring-primary/40' : ''"
  >
    <Handle type="target" :position="Position.Top" />
    <div class="flex items-center gap-1.5 px-2.5 py-1.5">
      <Wrench class="h-3 w-3 shrink-0 text-muted-foreground" />
      <span class="truncate font-mono text-xs">{{ node.label }}</span>
      <Loader2 v-if="node.status === 'running'" class="ml-auto h-3 w-3 shrink-0 animate-spin text-primary" />
      <span
        v-else
        class="ml-auto h-1.5 w-1.5 shrink-0 rounded-full"
        :class="node.status === 'completed' ? 'bg-emerald-500' : 'bg-red-400'"
      />
    </div>
    <div class="flex items-center justify-between border-t px-2.5 py-0.5 text-[10px] text-muted-foreground">
      <span>{{ node.status === 'failed' ? 'error' : node.status }}</span>
      <span v-if="duration" class="font-mono">{{ duration }}</span>
    </div>
    <Handle type="source" :position="Position.Bottom" />
  </div>
</template>
