<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, Copy, X } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import MarkdownRenderer from '@/components/chat/MarkdownRenderer.vue'
import { useCanvasStore } from '@/stores/canvas'
import { formatDuration, type CanvasNode } from '@/lib/canvas'

const store = useCanvasStore()
const node = computed(() => store.selectedNode)

const kindLabel = computed(() =>
  node.value?.kind === 'agent' ? 'agent' : 'tool call',
)

/** Pretty-print a JSON-looking string; falls back to the raw text. */
function pretty(value: string | undefined): string {
  if (!value) return ''
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

const argsText = computed(() => {
  const args = node.value?.args
  if (args === undefined || args === null) return ''
  try {
    return JSON.stringify(args, null, 2)
  } catch {
    return String(args)
  }
})

const resultText = computed(() => pretty(node.value?.result))

const reasoningEmpty = computed(() => {
  const n = node.value
  if (!n) return true
  return n.text === '' && n.thought === ''
})

const rootHint = computed(() => {
  const n = node.value
  return !!n && n.kind === 'agent' && !n.parentId
})

const rawJson = computed(() => {
  const n: CanvasNode | null = node.value
  if (!n) return ''
  const { text, thought, ...rest } = n
  return JSON.stringify({ ...rest, text_length: text.length, thought_length: thought.length }, null, 2)
})

const copied = ref(false)
async function copy(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1200)
  } catch {
    // clipboard unavailable (permissions/insecure context)
  }
}
</script>

<template>
  <div v-if="node" class="flex h-full flex-col overflow-hidden rounded-lg border bg-background/95 shadow-lg backdrop-blur">
    <div class="flex items-center gap-2 border-b px-3 py-2">
      <Badge :variant="node.kind === 'agent' ? 'default' : 'secondary'" class="text-[10px] uppercase">
        {{ kindLabel }}
      </Badge>
      <span class="truncate text-sm font-medium">{{ node.label }}</span>
      <Button
        variant="ghost"
        size="icon-xs"
        class="ml-auto shrink-0"
        aria-label="Close details"
        @click="store.select(null)"
      >
        <X class="h-3.5 w-3.5" />
      </Button>
    </div>

    <div class="flex items-center gap-2 border-b px-3 py-1.5 text-xs text-muted-foreground">
      <span
        class="h-1.5 w-1.5 rounded-full"
        :class="node.status === 'completed' ? 'bg-emerald-500' : node.status === 'running' ? 'bg-primary animate-pulse' : 'bg-red-400'"
      />
      <span>{{ node.status }}</span>
      <span v-if="node.durationMs !== undefined" class="font-mono">{{ formatDuration(node.durationMs) }}</span>
    </div>

    <Tabs default-value="overview" class="flex min-h-0 flex-1 flex-col">
      <TabsList class="mx-3 mt-2 grid grid-cols-3">
        <TabsTrigger value="overview" class="text-xs">Overview</TabsTrigger>
        <TabsTrigger value="reasoning" class="text-xs">Reasoning</TabsTrigger>
        <TabsTrigger value="raw" class="text-xs">Raw</TabsTrigger>
      </TabsList>

      <TabsContent value="overview" class="min-h-0 flex-1 overflow-y-auto px-3 pb-3 pt-2">
        <template v-if="node.goal">
          <p class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Goal</p>
          <p class="mb-3 whitespace-pre-wrap text-xs leading-relaxed">{{ node.goal }}</p>
        </template>
        <template v-if="node.summary">
          <p class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Result summary</p>
          <p class="mb-3 whitespace-pre-wrap text-xs leading-relaxed">{{ node.summary }}</p>
        </template>
        <template v-if="node.error">
          <p class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-red-400">Error</p>
          <p class="mb-3 whitespace-pre-wrap text-xs leading-relaxed text-red-400">{{ node.error }}</p>
        </template>
        <template v-if="argsText">
          <div class="mb-1 flex items-center justify-between">
            <p class="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Arguments</p>
            <Button variant="ghost" size="icon-xs" aria-label="Copy arguments" @click="copy(argsText)">
              <Copy class="h-3 w-3" />
            </Button>
          </div>
          <pre class="mb-3 max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/50 p-2 font-mono text-[11px] leading-relaxed">{{ argsText }}</pre>
        </template>
        <template v-if="resultText">
          <div class="mb-1 flex items-center justify-between">
            <p class="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Tool output</p>
            <Button variant="ghost" size="icon-xs" aria-label="Copy output" @click="copy(resultText)">
              <Copy class="h-3 w-3" />
            </Button>
          </div>
          <pre class="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/50 p-2 font-mono text-[11px] leading-relaxed">{{ resultText }}</pre>
        </template>
        <p v-if="!node.goal && !node.summary && !node.error && !argsText && !resultText" class="text-xs text-muted-foreground">
          No details recorded for this node.
        </p>
      </TabsContent>

      <TabsContent value="reasoning" class="min-h-0 flex-1 overflow-y-auto px-3 pb-3 pt-2">
        <p v-if="rootHint && reasoningEmpty" class="text-xs text-muted-foreground">
          The root agent's reasoning streams in the chat transcript - select a delegation node to see its internal
          thought process here.
        </p>
        <p v-else-if="reasoningEmpty" class="text-xs text-muted-foreground">No reasoning recorded for this node.</p>
        <template v-else>
          <div v-if="node.thought" class="mb-3">
            <p class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Thinking</p>
            <div class="rounded-md bg-muted/50 p-2">
              <MarkdownRenderer :content="node.thought" :streaming="node.status === 'running'" />
            </div>
          </div>
          <div v-if="node.text">
            <p class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Output</p>
            <MarkdownRenderer :content="node.text" :streaming="node.status === 'running'" />
          </div>
        </template>
      </TabsContent>

      <TabsContent value="raw" class="min-h-0 flex-1 overflow-y-auto px-3 pb-3 pt-2">
        <div class="mb-1 flex items-center justify-end">
          <Button variant="ghost" size="icon-xs" aria-label="Copy raw JSON" @click="copy(rawJson)">
            <Check v-if="copied" class="h-3 w-3 text-emerald-500" />
            <Copy v-else class="h-3 w-3" />
          </Button>
        </div>
        <pre class="whitespace-pre-wrap break-words rounded-md bg-muted/50 p-2 font-mono text-[11px] leading-relaxed">{{ rawJson }}</pre>
      </TabsContent>
    </Tabs>
  </div>
</template>
