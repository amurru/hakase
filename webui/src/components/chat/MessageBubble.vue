<script setup lang="ts">
import { computed, ref } from 'vue'
import { Bot, User, Copy, Check } from '@lucide/vue'
import type { ChatMessage } from '@/composables/useSSE'
import MarkdownRenderer from './MarkdownRenderer.vue'
import ThinkingBlock from './ThinkingBlock.vue'

const props = defineProps<{
  message: ChatMessage
  streaming?: boolean
}>()

const isUser = computed(() => props.message.role === 'user')
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
    class="flex gap-3 px-4 py-3"
    :class="isUser ? 'flex-row-reverse' : 'flex-row'"
  >
    <!-- Avatar -->
    <div
      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full"
      :class="isUser
        ? 'bg-primary/20 text-primary'
        : 'bg-muted text-muted-foreground'"
    >
      <User v-if="isUser" class="h-4 w-4" />
      <Bot v-else class="h-4 w-4" />
    </div>

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
          class="inline-block h-4 w-0.5 animate-pulse bg-foreground/50 ml-0.5 align-text-bottom"
        />
      </div>

      <!-- Copy button -->
      <button
        v-if="!streaming"
        type="button"
        class="flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-muted-foreground/70 transition-colors hover:bg-muted hover:text-muted-foreground"
        :title="copied ? 'Copied' : 'Copy message'"
        @click="copyContent"
      >
        <Check v-if="copied" class="h-3 w-3 text-emerald-500" />
        <Copy v-else class="h-3 w-3" />
      </button>
    </div>
  </div>
</template>
