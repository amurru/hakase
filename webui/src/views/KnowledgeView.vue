<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
// No Textarea component - using native textarea styled to match
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import {
  Plus,
  Search,
  RefreshCw,
  FileText,
  Edit3,
  Save,
  X,
  ExternalLink,
  Tag,
  Link2,
  AlertCircle,
} from '@lucide/vue'
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'
import ImageLightbox from '@/components/chat/ImageLightbox.vue'

// Types
interface KnowledgeNote {
  slug: string
  title: string
  summary?: string
  status?: string
  confidence?: string
  tags?: string[]
  aliases?: string[]
  created?: string
  updated?: string
  sources?: { url?: string; path?: string }[]
  related?: string[]
  metadata?: Record<string, string>
  body?: string
  backlinks?: string[]
  dangling?: string[]
}

interface SearchResult {
  note: KnowledgeNote
  score: number
  snippet?: string
}

// State
const notes = ref<KnowledgeNote[]>([])
const selectedSlug = ref<string>('')
const selectedNote = ref<KnowledgeNote | null>(null)
const loading = ref(false)
const error = ref('')
const searchQuery = ref('')
const searchResults = ref<SearchResult[]>([])
const isSearching = ref(false)

// Edit mode
const isEditing = ref(false)
const editBody = ref('')
const editTitle = ref('')
const editTags = ref('')

// Create dialog
const createOpen = ref(false)
const newTitle = ref('')
const newBody = ref('')
const newTags = ref('')

// Wikilink navigation
const navigationStack = ref<string[]>([])

// Image lightbox
const lightbox = ref<InstanceType<typeof ImageLightbox>>()

// Markdown renderer with wikilink support
const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
  breaks: true,
})

// Custom wikilink renderer - converts [[slug]] to clickable links
md.core.ruler.push('wikilinks', (state: any) => {
  const tokens = state.tokens
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i].type !== 'inline') continue
    const content = tokens[i].content
    const wikilinkRe = /\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]/g
    let match
    const replacements: { start: number; end: number; slug: string; label: string }[] = []

    while ((match = wikilinkRe.exec(content)) !== null) {
      const target = match[1].trim()
      const pipeIdx = match[0].indexOf('|')
      let label = target
      if (pipeIdx > 0) {
        label = match[0].substring(pipeIdx + 1, match[0].length - 2)
      }
      replacements.push({
        start: match.index,
        end: match.index + match[0].length,
        slug: target,
        label,
      })
    }

    if (replacements.length > 0) {
      // Replace from end to start to preserve indices
      for (let j = replacements.length - 1; j >= 0; j--) {
        const r = replacements[j]
        const before = content.substring(0, r.start)
        const after = content.substring(r.end)
        const linkText = `[${r.label}](#/wikilink/${encodeURIComponent(r.slug)})`
        tokens[i].content = before + linkText + after
      }
    }
  }
})

function renderMarkdown(content: string): string {
  if (!content) return ''
  const raw = md.render(content)
  return DOMPurify.sanitize(raw, {
    ADD_ATTR: ['target'],
  })
}

const renderedBody = computed(() => {
  if (!selectedNote.value?.body) return ''
  return renderMarkdown(selectedNote.value.body)
})

// Filtered notes based on search
const filteredNotes = computed(() => {
  if (isSearching.value && searchResults.value.length > 0) {
    return searchResults.value.map((r) => r.note)
  }
  if (!searchQuery.value) return notes.value
  const q = searchQuery.value.toLowerCase()
  return notes.value.filter(
    (n) =>
      n.title.toLowerCase().includes(q) ||
      n.slug.includes(q) ||
      (n.tags && n.tags.some((t) => t.toLowerCase().includes(q))) ||
      (n.summary && n.summary.toLowerCase().includes(q)),
  )
})

// API calls
async function loadNotes() {
  loading.value = true
  error.value = ''
  try {
    notes.value = await apiFetch<KnowledgeNote[]>('/knowledge')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load notes'
  } finally {
    loading.value = false
  }
}

async function selectNote(slug: string) {
  if (!slug) return
  // Push current to navigation stack
  if (selectedSlug.value) {
    navigationStack.value.push(selectedSlug.value)
  }
  selectedSlug.value = slug
  isEditing.value = false
  loading.value = true
  error.value = ''
  try {
    selectedNote.value = await apiFetch<KnowledgeNote>(`/knowledge/${encodeURIComponent(slug)}`)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load note'
    selectedNote.value = null
  } finally {
    loading.value = false
  }
}

async function handleSearch() {
  const q = searchQuery.value.trim()
  if (!q) {
    isSearching.value = false
    searchResults.value = []
    return
  }
  isSearching.value = true
  try {
    searchResults.value = await apiFetch<SearchResult[]>(
      `/knowledge/search?q=${encodeURIComponent(q)}`,
    )
  } catch {
    searchResults.value = []
  }
}

let searchDebounce: ReturnType<typeof setTimeout> | null = null
watch(searchQuery, () => {
  if (searchDebounce) clearTimeout(searchDebounce)
  searchDebounce = setTimeout(handleSearch, 300)
})

function goBack() {
  if (navigationStack.value.length > 0) {
    const prev = navigationStack.value.pop()!
    selectedSlug.value = prev
    selectNote(prev)
  }
}

// Edit mode
function startEditing() {
  if (!selectedNote.value) return
  isEditing.value = true
  editBody.value = selectedNote.value.body || ''
  editTitle.value = selectedNote.value.title || ''
  editTags.value = (selectedNote.value.tags || []).join(', ')
}

function cancelEditing() {
  isEditing.value = false
}

async function saveEdit() {
  if (!selectedNote.value) return
  const tags = editTags.value
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean)
  try {
    await apiFetch(`/knowledge/${encodeURIComponent(selectedNote.value.slug)}`, {
      method: 'PATCH',
      body: {
        title: editTitle.value,
        body: editBody.value,
        tags,
      },
    })
    isEditing.value = false
    await selectNote(selectedNote.value.slug)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to save note'
  }
}

// Create note
async function handleCreateNote() {
  if (!newTitle.value.trim()) return
  const tags = newTags.value
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean)
  try {
    const created = await apiFetch<KnowledgeNote>('/knowledge', {
      method: 'POST',
      body: {
        title: newTitle.value.trim(),
        body: newBody.value || `# ${newTitle.value.trim()}\n\n`,
        tags,
      },
    })
    createOpen.value = false
    newTitle.value = ''
    newBody.value = ''
    newTags.value = ''
    await loadNotes()
    if (created?.slug) {
      await selectNote(created.slug)
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to create note'
  }
}

// Handle wikilink clicks and image lightbox
function handleContentClick(e: MouseEvent) {
  const target = e.target as HTMLElement
  const img = target.closest('img')
  if (img) {
    lightbox.value?.show(img.currentSrc || img.src, img.alt || '')
    return
  }
  if (target.tagName === 'A') {
    const href = (target as HTMLAnchorElement).href
    if (href.includes('#/wikilink/')) {
      e.preventDefault()
      const slug = decodeURIComponent(href.split('#/wikilink/')[1])
      selectNote(slug)
    }
  }
}

// Status badge colors
function statusColor(status?: string): string {
  switch (status) {
    case 'permanent':
      return 'bg-green-500/20 text-green-400 border-green-500/30'
    case 'draft':
      return 'bg-yellow-500/20 text-yellow-400 border-yellow-500/30'
    case 'archived':
      return 'bg-gray-500/20 text-gray-400 border-gray-500/30'
    default:
      return 'bg-blue-500/20 text-blue-400 border-blue-500/30'
  }
}

function statusLabel(status?: string): string {
  return status || 'draft'
}

// Init
onMounted(loadNotes)
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <h1 class="text-lg font-semibold">Knowledge</h1>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="loadNotes">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        </Button>
        <Button size="sm" @click="createOpen = true">
          <Plus class="mr-1 h-4 w-4" />
          New Note
        </Button>
      </div>
    </div>

    <!-- Main content: sidebar + viewer -->
    <div class="flex flex-1 overflow-hidden">
      <!-- Note list sidebar -->
      <div class="w-72 shrink-0 border-r border-border bg-sidebar-background">
        <!-- Search -->
        <div class="border-b border-border p-3">
          <div class="relative">
            <Search class="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              v-model="searchQuery"
              placeholder="Search notes..."
              class="h-8 pl-8 text-sm"
            />
          </div>
        </div>

        <!-- Note list -->
        <ScrollArea class="h-full">
          <div class="p-2">
            <!-- Loading -->
            <div v-if="loading && notes.length === 0" class="p-4 text-center text-sm text-muted-foreground">
              Loading...
            </div>

            <!-- Error -->
            <div v-else-if="error && notes.length === 0" class="p-4">
              <div class="flex items-center gap-2 text-sm text-destructive">
                <AlertCircle class="h-4 w-4 shrink-0" />
                <span>{{ error }}</span>
              </div>
              <Button variant="ghost" size="sm" class="mt-2" @click="loadNotes">
                Retry
              </Button>
            </div>

            <!-- Empty state -->
            <div v-else-if="filteredNotes.length === 0" class="p-4 text-center text-sm text-muted-foreground">
              <FileText class="mx-auto mb-2 h-6 w-6 opacity-50" />
              <p v-if="searchQuery">No matching notes</p>
              <p v-else>No notes yet</p>
            </div>

            <!-- Note items -->
            <button
              v-for="note in filteredNotes"
              :key="note.slug"
              class="w-full rounded-md px-3 py-2 text-left transition-colors hover:bg-accent"
              :class="{ 'bg-accent': selectedSlug === note.slug }"
              @click="selectNote(note.slug)"
            >
              <div class="flex items-start justify-between gap-2">
                <span class="text-sm font-medium leading-snug line-clamp-1">
                  {{ note.title }}
                </span>
                <Badge
                  v-if="note.status"
                  :class="statusColor(note.status)"
                  variant="outline"
                  class="shrink-0 text-[10px]"
                >
                  {{ statusLabel(note.status) }}
                </Badge>
              </div>
              <p
                v-if="note.summary"
                class="mt-0.5 text-xs text-muted-foreground line-clamp-2"
              >
                {{ note.summary }}
              </p>
              <div v-if="note.tags && note.tags.length > 0" class="mt-1 flex flex-wrap gap-1">
                <Badge
                  v-for="tag in note.tags.slice(0, 3)"
                  :key="tag"
                  variant="secondary"
                  class="px-1.5 py-0 text-[10px]"
                >
                  {{ tag }}
                </Badge>
                <Badge
                  v-if="note.tags.length > 3"
                  variant="secondary"
                  class="px-1.5 py-0 text-[10px]"
                >
                  +{{ note.tags.length - 3 }}
                </Badge>
              </div>
            </button>
          </div>
        </ScrollArea>
      </div>

      <!-- Note viewer -->
      <div class="flex flex-1 flex-col overflow-hidden">
        <!-- No note selected -->
        <div v-if="!selectedSlug" class="flex flex-1 items-center justify-center text-muted-foreground">
          <div class="text-center">
            <FileText class="mx-auto mb-2 h-8 w-8 opacity-50" />
            <p class="text-sm">Select a note to view</p>
          </div>
        </div>

        <!-- Note header bar -->
        <div v-else class="flex shrink-0 items-center justify-between border-b border-border px-4 py-2">
          <div class="flex items-center gap-2 overflow-hidden">
            <Button
              v-if="navigationStack.length > 0"
              variant="ghost"
              size="sm"
              class="h-7 px-2"
              @click="goBack"
            >
              <ExternalLink class="h-3.5 w-3.5 rotate-180" />
            </Button>
            <span class="truncate text-sm font-medium">{{ selectedNote?.title }}</span>
            <Badge
              v-if="selectedNote?.status"
              :class="statusColor(selectedNote.status)"
              variant="outline"
              class="shrink-0 text-[10px]"
            >
              {{ statusLabel(selectedNote.status) }}
            </Badge>
          </div>
          <div class="flex items-center gap-1">
            <Button
              v-if="!isEditing"
              variant="ghost"
              size="sm"
              class="h-7 px-2"
              @click="startEditing"
            >
              <Edit3 class="h-3.5 w-3.5" />
            </Button>
            <template v-else>
              <Button variant="ghost" size="sm" class="h-7 px-2" @click="cancelEditing">
                <X class="h-3.5 w-3.5" />
              </Button>
              <Button variant="ghost" size="sm" class="h-7 px-2" @click="saveEdit">
                <Save class="h-3.5 w-3.5" />
              </Button>
            </template>
          </div>
        </div>

        <!-- Content area -->
        <div class="flex-1 overflow-auto">
          <!-- Loading -->
          <div v-if="loading && selectedSlug" class="flex h-full items-center justify-center text-muted-foreground">
            <span class="text-sm">Loading...</span>
          </div>

          <!-- Error -->
          <div v-else-if="error && selectedSlug" class="flex h-full items-center justify-center">
            <div class="text-center">
              <AlertCircle class="mx-auto mb-2 h-6 w-6 text-destructive" />
              <p class="text-sm text-destructive">{{ error }}</p>
            </div>
          </div>

          <!-- Edit mode -->
          <div v-else-if="isEditing" class="flex h-full flex-col p-4">
            <div class="mb-3 grid gap-2">
              <Label for="edit-title" class="text-xs">Title</Label>
              <Input id="edit-title" v-model="editTitle" class="h-8 text-sm" />
            </div>
            <div class="mb-3 grid gap-2">
              <Label for="edit-tags" class="text-xs">Tags (comma-separated)</Label>
              <Input id="edit-tags" v-model="editTags" class="h-8 text-sm" placeholder="tag1, tag2, tag3" />
            </div>
            <Label for="edit-body" class="mb-1 text-xs">Content (Markdown)</Label>
            <textarea
              id="edit-body"
              v-model="editBody"
              class="flex-1 resize-none rounded-md border border-input bg-background px-3 py-2 font-mono text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              placeholder="Write your note in Markdown..."
            />
          </div>

          <!-- View mode -->
          <div v-else-if="selectedNote" class="flex h-full flex-col">
            <!-- Metadata bar -->
            <div class="flex flex-wrap items-center gap-3 border-b border-border px-6 py-2 text-xs text-muted-foreground">
              <span v-if="selectedNote.created">Created: {{ selectedNote.created }}</span>
              <span v-if="selectedNote.updated">Updated: {{ selectedNote.updated }}</span>
              <span v-if="selectedNote.confidence">Confidence: {{ selectedNote.confidence }}</span>
            </div>

            <!-- Tags and aliases -->
            <div
              v-if="(selectedNote.tags && selectedNote.tags.length > 0) || (selectedNote.aliases && selectedNote.aliases.length > 0)"
              class="flex flex-wrap items-center gap-2 border-b border-border px-6 py-2"
            >
              <template v-if="selectedNote.tags && selectedNote.tags.length > 0">
                <Tag class="h-3.5 w-3.5 text-muted-foreground" />
                <Badge
                  v-for="tag in selectedNote.tags"
                  :key="tag"
                  variant="secondary"
                  class="px-1.5 py-0 text-xs"
                >
                  {{ tag }}
                </Badge>
              </template>
              <template v-if="selectedNote.aliases && selectedNote.aliases.length > 0">
                <Link2 class="h-3.5 w-3.5 text-muted-foreground" />
                <span
                  v-for="alias in selectedNote.aliases"
                  :key="alias"
                  class="text-xs text-muted-foreground"
                >
                  {{ alias }}
                </span>
              </template>
            </div>

            <!-- Backlinks -->
            <div
              v-if="selectedNote.backlinks && selectedNote.backlinks.length > 0"
              class="border-b border-border px-6 py-2 text-xs text-muted-foreground"
            >
              <span class="font-medium">Backlinks:</span>
              {{ selectedNote.backlinks.join(', ') }}
            </div>

            <!-- Dangling links -->
            <div
              v-if="selectedNote.dangling && selectedNote.dangling.length > 0"
              class="border-b border-border bg-yellow-500/10 px-6 py-2 text-xs text-yellow-400"
            >
              <AlertCircle class="mr-1 inline h-3 w-3" />
              Dangling links: {{ selectedNote.dangling.join(', ') }}
            </div>

            <!-- Metadata -->
            <div
              v-if="selectedNote.metadata && Object.keys(selectedNote.metadata).length > 0"
              class="border-b border-border px-6 py-2"
            >
              <div class="text-xs font-medium text-muted-foreground">Metadata</div>
              <div class="mt-1 flex flex-wrap gap-2">
                <Badge
                  v-for="(value, key) in selectedNote.metadata"
                  :key="key"
                  variant="outline"
                  class="text-[10px]"
                >
                  {{ key }}: {{ value }}
                </Badge>
              </div>
            </div>

            <!-- Rendered body -->
            <div
              class="flex-1 overflow-auto p-6"
              @click="handleContentClick"
            >
              <div
                class="markdown-body prose prose-invert prose-sm max-w-none
                       prose-headings:tracking-tight prose-headings:text-foreground
                       prose-p:text-foreground/90 prose-p:leading-relaxed
                       prose-a:text-primary prose-a:no-underline hover:prose-a:underline
                       prose-strong:text-foreground prose-strong:font-semibold
                       prose-code:text-amber-300/90 prose-code:font-mono prose-code:text-[0.85em]
                       prose-code:bg-muted prose-code:px-1.5 prose-code:py-0.5 prose-code:rounded
                       prose-code:before:content-none prose-code:after:content-none
                       prose-pre:bg-transparent prose-pre:p-0 prose-pre:my-4
                       prose-li:text-foreground/90
                       prose-blockquote:border-l-primary/40 prose-blockquote:text-muted-foreground
                       prose-hr:border-border"
                v-html="renderedBody"
              />
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Create note dialog -->
    <Dialog v-model:open="createOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create Note</DialogTitle>
        </DialogHeader>
        <div class="grid gap-4 py-2">
          <div class="grid gap-2">
            <Label for="create-title">Title</Label>
            <Input
              id="create-title"
              v-model="newTitle"
              placeholder="Note title"
              @keydown.enter="handleCreateNote"
            />
          </div>
          <div class="grid gap-2">
            <Label for="create-tags">Tags (comma-separated)</Label>
            <Input id="create-tags" v-model="newTags" placeholder="tag1, tag2, tag3" />
          </div>
          <div class="grid gap-2">
            <Label for="create-body">Content (Markdown)</Label>
            <textarea
              id="create-body"
              v-model="newBody"
              class="min-h-[150px] rounded-md border border-input bg-background px-3 py-2 font-mono text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              placeholder="Write your note in Markdown..."
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" @click="createOpen = false">Cancel</Button>
          <Button @click="handleCreateNote" :disabled="!newTitle.trim()">Create</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <ImageLightbox ref="lightbox" />
  </div>
</template>

<style scoped>
.markdown-body :deep(pre) {
  background: hsl(var(--muted));
  border-radius: 0.5rem;
  padding: 1rem;
  overflow-x: auto;
  margin: 1rem 0;
}

.markdown-body :deep(pre code) {
  background: transparent !important;
  padding: 0 !important;
  color: hsl(var(--foreground));
  font-size: 0.8125rem;
  line-height: 1.7;
}

.markdown-body :deep(table) {
  width: 100%;
  border-collapse: collapse;
  margin: 1rem 0;
  font-size: 0.8125rem;
}

.markdown-body :deep(th),
.markdown-body :deep(td) {
  border: 1px solid hsl(var(--border));
  padding: 0.5rem 0.75rem;
  text-align: left;
}

.markdown-body :deep(th) {
  background: hsl(var(--muted));
  font-weight: 600;
}

.markdown-body :deep(img) {
  display: block;
  max-width: 100%;
  margin: 1.25rem auto;
  border-radius: 0.5rem;
  cursor: zoom-in;
}

.markdown-body :deep(a) {
  color: hsl(var(--primary));
}

.markdown-body :deep(ul) {
  list-style-type: disc;
  padding-left: 1.5rem;
}

.markdown-body :deep(ol) {
  list-style-type: decimal;
  padding-left: 1.5rem;
}
</style>
