<script setup lang="ts">
import { ref, watch } from 'vue'
import { Paperclip, X, Search } from '@lucide/vue'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { apiGet } from '@/lib/api'

export interface FileAttachment {
  id: string
  kind: 'file' | 'image'
  name: string
  path: string
  mime: string
  label: string
  data?: string // base64 for pasted images
}

const props = defineProps<{
  disabled?: boolean
}>()

const emit = defineEmits<{
  'update:attachments': [attachments: FileAttachment[]]
}>()

const attachments = ref<FileAttachment[]>([])
const pickerOpen = ref(false)
const searchQuery = ref('')
const fileResults = ref<Array<{ name: string; path: string; mime: string }>>([])
const loading = ref(false)
const imageCounter = ref(0)

let searchTimeout: ReturnType<typeof setTimeout> | null = null

function openPicker() {
  searchQuery.value = ''
  fileResults.value = []
  pickerOpen.value = true
  fetchFiles('')
}

async function fetchFiles(query: string) {
  loading.value = true
  try {
    const results = await apiGet<Array<{ name: string; path: string; mime: string }>>(
      `/files/browse?q=${encodeURIComponent(query)}`,
    )
    fileResults.value = results
  } catch {
    fileResults.value = []
  } finally {
    loading.value = false
  }
}

watch(searchQuery, (q) => {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => fetchFiles(q), 200)
})

function selectFile(file: { name: string; path: string; mime: string }) {
  const id = `file-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
  attachments.value.push({
    id,
    kind: 'file',
    name: file.name,
    path: file.path,
    mime: file.mime,
    label: `@${file.name}`,
  })
  emit('update:attachments', [...attachments.value])
  pickerOpen.value = false
}

function removeAttachment(id: string) {
  attachments.value = attachments.value.filter((a) => a.id !== id)
  emit('update:attachments', [...attachments.value])
}

function removeLastAttachment() {
  if (attachments.value.length === 0) return false
  attachments.value.pop()
  emit('update:attachments', [...attachments.value])
  return true
}

function addImageAttachment(name: string, mime: string, base64: string) {
  imageCounter.value++
  const id = `img-${Date.now()}-${imageCounter.value}`
  attachments.value.push({
    id,
    kind: 'image',
    name: name || `image ${imageCounter.value}`,
    path: '',
    mime: mime || 'image/png',
    label: `[image ${imageCounter.value}]`,
    data: base64,
  })
  emit('update:attachments', [...attachments.value])
}

// Expose for ChatInput to call on backspace and paste
defineExpose({ removeLastAttachment, addImageAttachment, openPicker })
</script>

<template>
  <div>
    <!-- Chips row -->
    <div
      v-if="attachments.length > 0"
      class="flex flex-wrap gap-1.5 px-1 pb-1.5"
    >
      <span
        v-for="att in attachments"
        :key="att.id"
        class="inline-flex items-center gap-1 rounded-md bg-primary/10 px-2 py-0.5 text-xs text-primary"
      >
        <Paperclip v-if="att.kind === 'file'" class="h-3 w-3" />
        <span>{{ att.label }}</span>
        <button
          class="ml-0.5 rounded-full p-0.5 hover:bg-primary/20"
          @click="removeAttachment(att.id)"
        >
          <X class="h-2.5 w-2.5" />
        </button>
      </span>
    </div>

    <!-- @ trigger dialog -->
    <Dialog v-model:open="pickerOpen">
      <DialogContent class="sm:max-w-md p-0 gap-0">
        <DialogHeader class="px-4 pt-4 pb-2">
          <DialogTitle>Attach File</DialogTitle>
        </DialogHeader>

        <!-- Search -->
        <div class="px-4 pb-2">
          <div class="relative">
            <Search class="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              v-model="searchQuery"
              placeholder="Search files..."
              class="pl-8"
            />
          </div>
        </div>

        <!-- File list -->
        <ScrollArea class="h-[300px] px-4 pb-4">
          <div v-if="loading" class="py-8 text-center text-sm text-muted-foreground">
            Loading...
          </div>
          <div v-else-if="fileResults.length === 0" class="py-8 text-center text-sm text-muted-foreground">
            No files found
          </div>
          <div v-else class="space-y-0.5">
            <button
              v-for="file in fileResults"
              :key="file.path"
              class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-muted/50 transition-colors"
              @click="selectFile(file)"
            >
              <Paperclip class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <span class="truncate">{{ file.name }}</span>
              <span class="ml-auto shrink-0 text-xs text-muted-foreground/60">{{ file.mime }}</span>
            </button>
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  </div>
</template>
