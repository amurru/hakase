<script setup lang="ts">
import { ref, nextTick, onMounted } from 'vue'
import { Send, Paperclip } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import AttachmentPicker, { type FileAttachment } from './AttachmentPicker.vue'

const emit = defineEmits<{
  send: [content: string, attachments?: FileAttachment[]]
}>()

defineProps<{
  disabled?: boolean
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)
const content = ref('')
const attachments = ref<FileAttachment[]>([])
const pickerRef = ref<InstanceType<typeof AttachmentPicker> | null>(null)

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
    return
  }

  // Backspace on empty input removes last attachment chip
  if (e.key === 'Backspace' && content.value === '' && attachments.value.length > 0) {
    e.preventDefault()
    pickerRef.value?.removeLastAttachment()
    return
  }
}

function handlePaste(event: ClipboardEvent) {
  const items = event.clipboardData?.items
  if (!items) return

  for (const item of items) {
    if (item.type.startsWith('image/')) {
      event.preventDefault()
      const blob = item.getAsFile()
      if (!blob) continue

      // 10 MB limit
      if (blob.size > 10 * 1024 * 1024) return

      const reader = new FileReader()
      reader.onload = () => {
        const dataUrl = reader.result as string
        const base64 = dataUrl.split(',')[1] ?? ''
        pickerRef.value?.addImageAttachment(blob.name || '', blob.type || 'image/png', base64)
      }
      reader.readAsDataURL(blob)
      return
    }
  }
}

function handleSend() {
  const trimmed = content.value.trim()
  if (!trimmed && attachments.value.length === 0) return
  if (trimmed) {
    emit('send', trimmed, attachments.value.length > 0 ? [...attachments.value] : undefined)
  } else if (attachments.value.length > 0) {
    // Send only attachments (no text)
    emit('send', '', [...attachments.value])
  }
  content.value = ''
  nextTick(autoResize)
}

onMounted(() => {
  textareaRef.value?.focus()
})
</script>

<template>
  <div class="border-t border-border bg-background p-4">
    <!-- Attachment picker (chips + @ trigger) -->
    <AttachmentPicker
      ref="pickerRef"
      :disabled="disabled"
      @update:attachments="attachments = $event"
    />

    <div class="flex items-end gap-2">
      <Button
        variant="ghost"
        size="icon"
        class="h-10 w-10 shrink-0 rounded-xl text-muted-foreground hover:text-foreground"
        :disabled="disabled"
        @click="pickerRef?.openPicker()"
      >
        <Paperclip class="h-4 w-4" />
      </Button>
      <textarea
        ref="textareaRef"
        v-model="content"
        placeholder="Type a message..."
        rows="1"
        class="flex-1 resize-none rounded-xl border border-border bg-muted/50 px-4 py-3 text-sm text-foreground placeholder:text-muted-foreground/50 outline-none transition-colors focus:border-primary/50 focus:ring-1 focus:ring-primary/30"
        :disabled="disabled"
        @input="autoResize"
        @keydown="handleKeydown"
        @paste="handlePaste"
      />
      <Button
        size="icon"
        class="h-10 w-10 shrink-0 rounded-xl"
        :disabled="(!content.trim() && attachments.length === 0) || disabled"
        @click="handleSend"
      >
        <Send class="h-4 w-4" />
      </Button>
    </div>

    <p class="mt-1.5 text-[11px] text-muted-foreground/50">
      Enter to send, Shift+Enter for newline, @ to attach files, Ctrl+V to paste images
    </p>
  </div>
</template>
