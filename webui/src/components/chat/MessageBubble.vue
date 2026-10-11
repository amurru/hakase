<script setup lang="ts">
import { computed, ref } from 'vue'
import { Bot, User, Copy, Check, History } from '@lucide/vue'
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
  <div
    class="group flex gap-3 px-4"
    :class="[isUser ? 'flex-row-reverse' : 'flex-row', compact ? 'py-1' : 'py-3']"
    :data-message-id="message.id"
  >
    <!-- Avatar: hidden on grouped follow-ups, keeping a spacer so the
         bubble edge stays aligned with the group. -->
    <div
      v-if="!compact"
      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full"
      :class="isUser
        ? 'bg-primary/20 text-primary'
        : 'bg-muted text-muted-foreground'"
    >
      <User v-if="isUser" class="h-4 w-4" />
      <Bot v-else class="h-4 w-4" />
    </div>
    <div v-else class="w-8 shrink-0" aria-hidden="true" />

    <!-- Content -->
    <div
      class="flex max-w-[80%] flex-col gap-1"
      :class="isUser ? 'items-end' : 'items-start'"
    >
      <!-- Bubble -->
      <div
        class="rounded-2xl px-4 py-2.5 text-sm leading-relaxed"
        :class="isUser
          ? 'bg-primary text-primary-foreground rounded-br-md'
          : 'bg-muted text-foreground rounded-bl-md'"
      >
        <!-- Thinking block (agent only) -->
        <ThinkingBlock
          v-if="!isUser && message.thinking"
          :content="message.thinking"
        />

        <!-- Message content -->
        <div v-if="isUser" class="whitespace-pre-wrap">{{ message.content }}</div>
        <MarkdownRenderer v-else :content="message.content" :streaming="streaming" />

        <!-- Streaming indicator -->
        <span
          v-if="streaming && !isUser && message.content"
          class="inline-block h-4 w-0.5 motion-safe:animate-pulse bg-foreground/50 ml-0.5 align-text-bottom"
        />
      </div>

      <!-- Hover toolbar: timestamp + copy + restore in one row. Hidden
           until hover/focus on fine pointers; always visible on touch
           (no hover there) so the actions are never stranded. -->
      <div
        v-if="!streaming"
        class="flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 pointer-coarse:opacity-100"
        :class="isUser ? 'justify-end' : 'justify-start'"
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
  </div>
</template>
