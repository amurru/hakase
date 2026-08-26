<script setup lang="ts">
import { ref, computed, nextTick, onMounted } from 'vue'
import { Send, Paperclip } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import AttachmentPicker, { type FileAttachment } from './AttachmentPicker.vue'
import { suggestCommands, type SlashCommand } from '@/lib/slash'

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

// --- Slash command palette -------------------------------------------------
// Visible only while the value is exactly "/<token>" (no space yet), so a
// completion to "/name " naturally closes it and the next Enter sends.
const activeIdx = ref(0)
const dismissed = ref(false)
const suggestions = computed(() => suggestCommands(content.value))
const showPalette = computed(() => !dismissed.value && suggestions.value.length > 0)

function resetPalette() {
  activeIdx.value = 0
  dismissed.value = false
}

function completeCommand(cmd: SlashCommand) {
  content.value = `/${cmd.name} `
  dismissed.value = true
  nextTick(() => {
    autoResize()
    textareaRef.value?.focus()
  })
}

function onPaletteMousedown(e: MouseEvent, cmd: SlashCommand) {
  e.preventDefault() // keep textarea focus
  completeCommand(cmd)
}
// ---------------------------------------------------------------------------

function autoResize() {
  const el = textareaRef.value
  if (!el) return
  el.style.height = 'auto'
  const maxHeight = 8 * 24 // ~8 lines at ~24px line height
  el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`
}

function handleKeydown(e: KeyboardEvent) {
  // Palette navigation takes precedence while open.
  if (showPalette.value) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      activeIdx.value = (activeIdx.value + 1) % suggestions.value.length
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      activeIdx.value = (activeIdx.value - 1 + suggestions.value.length) % suggestions.value.length
      return
    }
    if (e.key === 'Tab' || e.key === 'Enter') {
      e.preventDefault()
      completeCommand(suggestions.value[activeIdx.value]!)
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      dismissed.value = true
      return
    }
  }

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
  // Clear consumed chips so they are not re-sent with the next message
  // (mirrors the TUI, which resets m.attachments after building parts).
  pickerRef.value?.clearAll()
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
      <div class="relative flex-1">
        <!-- Slash command palette -->
        <ul
          v-if="showPalette"
          class="absolute bottom-full left-0 right-0 z-10 mb-2 max-h-64 overflow-y-auto rounded-xl border border-border bg-popover py-1 shadow-lg"
          role="listbox"
          aria-label="Slash commands"
        >
          <li
            v-for="(cmd, i) in suggestions"
            :key="cmd.name"
            role="option"
            :aria-selected="i === activeIdx"
            class="flex cursor-pointer items-baseline gap-2 px-3 py-1.5 text-sm"
            :class="i === activeIdx ? 'bg-accent text-accent-foreground' : 'text-foreground'"
            @mousedown="onPaletteMousedown($event, cmd)"
            @mousemove="activeIdx = i"
          >
            <span class="font-mono font-medium">/{{ cmd.name }}</span>
            <span v-if="cmd.args" class="font-mono text-[11px] text-muted-foreground">{{ cmd.args }}</span>
            <span class="ml-auto truncate pl-2 text-[11px] text-muted-foreground">{{ cmd.description }}</span>
          </li>
        </ul>
        <textarea
          ref="textareaRef"
          v-model="content"
          placeholder="Type a message... / for commands"
          rows="1"
          class="w-full resize-none rounded-xl border border-border bg-muted/50 px-4 py-3 text-sm text-foreground placeholder:text-muted-foreground/50 outline-none transition-colors focus:border-primary/50 focus:ring-1 focus:ring-primary/30"
          :disabled="disabled"
          @input="autoResize(); resetPalette()"
          @keydown="handleKeydown"
          @paste="handlePaste"
        />
      </div>
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
      Enter to send, Shift+Enter for newline, / for commands, @ to attach files, Ctrl+V to paste images
    </p>
  </div>
</template>
