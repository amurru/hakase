<script setup lang="ts">
import { ref, nextTick, onMounted } from 'vue'
import { Send } from '@lucide/vue'
import { Button } from '@/components/ui/button'

const emit = defineEmits<{
  send: [content: string]
}>()

defineProps<{
  disabled?: boolean
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)
const content = ref('')

function autoResize() {
  const el = textareaRef.value
  if (!el) return
  el.style.height = 'auto'
  const maxHeight = 8 * 24 // ~8 lines at ~24px line height
  el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    handleSend()
  }
}

function handleSend() {
  const trimmed = content.value.trim()
  if (!trimmed) return
  emit('send', trimmed)
  content.value = ''
  nextTick(autoResize)
}

onMounted(() => {
  textareaRef.value?.focus()
})
</script>

<template>
  <div class="border-t border-border bg-background p-4">
    <!-- Attachment slot stub for task 35 -->
    <slot name="attachments" />

    <div class="flex items-end gap-2">
      <textarea
        ref="textareaRef"
        v-model="content"
        placeholder="Type a message..."
        rows="1"
        class="flex-1 resize-none rounded-xl border border-border bg-muted/50 px-4 py-3 text-sm text-foreground placeholder:text-muted-foreground/50 outline-none transition-colors focus:border-primary/50 focus:ring-1 focus:ring-primary/30"
        :disabled="disabled"
        @input="autoResize"
        @keydown="handleKeydown"
      />
      <Button
        size="icon"
        class="h-10 w-10 shrink-0 rounded-xl"
        :disabled="!content.trim() || disabled"
        @click="handleSend"
      >
        <Send class="h-4 w-4" />
      </Button>
    </div>

    <p class="mt-1.5 text-[11px] text-muted-foreground/50">
      Enter to send, Shift+Enter for newline
    </p>
  </div>
</template>
