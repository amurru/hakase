<script setup lang="ts">
import { computed } from 'vue'
import { Bot, User } from '@lucide/vue'
import type { ChatMessage } from '@/composables/useSSE'
import MarkdownRenderer from './MarkdownRenderer.vue'
import ThinkingBlock from './ThinkingBlock.vue'

const props = defineProps<{
  message: ChatMessage
  streaming?: boolean
}>()

const isUser = computed(() => props.message.role === 'user')
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
        <MarkdownRenderer v-else :content="message.content" />

        <!-- Streaming indicator -->
        <span
          v-if="streaming && !isUser && message.content"
          class="inline-block h-4 w-0.5 animate-pulse bg-foreground/50 ml-0.5 align-text-bottom"
        />
      </div>
    </div>
  </div>
</template>
