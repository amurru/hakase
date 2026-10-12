<script setup lang="ts">
import { computed, ref } from 'vue'
import { Copy, Check, History } from '@lucide/vue'
import type { ChatMessage } from '@/composables/useSSE'
import MarkdownRenderer from './MarkdownRenderer.vue'
import ThinkingBlock from './ThinkingBlock.vue'

const props = defineProps<{
  message: ChatMessage
  streaming?: boolean
  compact?: boolean
}>()

// Restore-to-here is offered on persisted user prompts only: the snapshot
// taken just before that prompt is the rewind point (issue #21). Live
// messages (no sequence yet) offer it after the next history reload.
const canRestore = computed(
  () => props.message.role === 'user' && !props.streaming && props.message.sequence !== undefined,
)
const emit = defineEmits<{ restore: [] }>()

const isUser = computed(() => props.message.role === 'user')

// Short clock time for the hover toolbar (HH:MM in locale order).
const timeLabel = computed(() => {
  if (!props.message.timestamp) return ''
  return new Date(props.message.timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })
})
const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | undefined

async function copyContent() {
  try {
    await navigator.clipboard.writeText(props.message.content)
  } catch {
    const textarea = document.createElement('textarea')
    textarea.value = props.message.content
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    document.execCommand('copy')
    document.body.removeChild(textarea)
  }
  copied.value = true
  clearTimeout(copyTimer)
  copyTimer = setTimeout(() => {
    copied.value = false
  }, 2000)
}
</script>

<template>
  <!-- ChatGPT pattern: the assistant renders as plain full-measure text
       (no bubble, no avatar); the user keeps a right-aligned bubble.
       Grouped follow-ups only tighten spacing. -->
  <div
    class="group flex px-4"
    :class="[isUser ? 'justify-end' : 'justify-start', compact ? 'py-1' : 'py-3']"
    :data-message-id="message.id"
  >
    <!-- User bubble -->
    <div
      v-if="isUser"
      class="flex max-w-[80%] flex-col items-end gap-1"
    >
      <div class="rounded-2xl rounded-br-md bg-primary px-4 py-2.5 text-sm leading-relaxed text-primary-foreground">
        <div class="whitespace-pre-wrap">{{ message.content }}</div>
      </div>
      <div
        v-if="!streaming"
        class="flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 pointer-coarse:opacity-100"
      >
        <span v-if="timeLabel" class="mr-1 text-[11px] tabular-nums text-muted-foreground/50">
          {{ timeLabel }}
        </span>
        <button
          type="button"
          class="flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-muted hover:text-muted-foreground"
          :title="copied ? 'Copied' : 'Copy message'"
          :aria-label="copied ? 'Copied' : 'Copy message'"
          @click="copyContent"
        >
          <Check v-if="copied" class="h-3 w-3 text-emerald-500" />
          <Copy v-else class="h-3 w-3" />
        </button>
        <button
          v-if="canRestore"
          type="button"
          class="flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-muted hover:text-muted-foreground"
          title="Restore conversation to before this message"
          aria-label="Restore conversation to before this message"
          @click="emit('restore')"
        >
          <History class="h-3 w-3" />
        </button>
      </div>
    </div>

    <!-- Assistant plain-text row -->
    <div
      v-else
      class="flex min-w-0 flex-1 flex-col items-start gap-1"
    >
      <div class="w-full min-w-0 text-sm leading-relaxed">
        <ThinkingBlock
          v-if="message.thinking"
          :content="message.thinking"
        />
        <MarkdownRenderer :content="message.content" :streaming="streaming" />
        <!-- Streaming indicator -->
        <span
          v-if="streaming && message.content"
          class="inline-block h-4 w-0.5 motion-safe:animate-pulse bg-foreground/50 ml-0.5 align-text-bottom"
        />
      </div>
      <div
        v-if="!streaming"
        class="flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 pointer-coarse:opacity-100"
      >
        <span v-if="timeLabel" class="mr-1 text-[11px] tabular-nums text-muted-foreground/50">
          {{ timeLabel }}
        </span>
        <button
          type="button"
          class="flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-muted hover:text-muted-foreground"
          :title="copied ? 'Copied' : 'Copy message'"
          :aria-label="copied ? 'Copied' : 'Copy message'"
          @click="copyContent"
        >
          <Check v-if="copied" class="h-3 w-3 text-emerald-500" />
          <Copy v-else class="h-3 w-3" />
        </button>
      </div>
    </div>
  </div>
</template>
