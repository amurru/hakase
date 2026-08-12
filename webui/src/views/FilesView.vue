<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import TreeItem from '@/components/TreeItem.vue'
import {
  FileCode,
  FileImage,
  File,
  Download,
  RefreshCw,
  AlertCircle,
  Copy,
  Check,
} from '@lucide/vue'
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'
import hljs from 'highlight.js/lib/core'
import go from 'highlight.js/lib/languages/go'
import python from 'highlight.js/lib/languages/python'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import rust from 'highlight.js/lib/languages/rust'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import markdown from 'highlight.js/lib/languages/markdown'
import 'highlight.js/styles/github-dark.css'

hljs.registerLanguage('go', go)
hljs.registerLanguage('python', python)
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('rust', rust)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('json', json)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('css', css)
hljs.registerLanguage('markdown', markdown)

// -- Types --

interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
}

interface FileContent {
  path: string
  name: string
  content?: string
  size: number
  mime: string
  is_binary: boolean
  truncated?: boolean
}

interface TreeNode {
  entry: FileEntry
  children: TreeNode[] | null
  expanded: boolean
  loading: boolean
  error: string | null
}

// -- State --

const treeRoots = ref<TreeNode[]>([])
const selectedPath = ref<string>('')
const fileContent = ref<FileContent | null>(null)
const loading = ref(false)
const error = ref<string>('')
const copied = ref(false)

// -- Markdown renderer --

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
})

// -- Helpers --

function getFileIcon(name: string) {
  const ext = name.split('.').pop()?.toLowerCase() || ''
  if (['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico'].includes(ext)) return FileImage
  if (
    [
      'js', 'ts', 'tsx', 'jsx', 'vue', 'go', 'py', 'rs', 'rb', 'java', 'c', 'cpp', 'h',
      'sh', 'bash', 'zsh', 'yaml', 'yml', 'toml', 'json', 'xml', 'html', 'css', 'scss',
    ].includes(ext)
  )
    return FileCode
  return File
}

function getLanguage(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() || ''
  const map: Record<string, string> = {
    go: 'go', py: 'python', js: 'javascript', ts: 'typescript', tsx: 'typescript',
    jsx: 'javascript', vue: 'html', rs: 'rust', rb: 'ruby', java: 'java',
    c: 'c', cpp: 'cpp', h: 'c', sh: 'bash', bash: 'bash', zsh: 'bash',
    yaml: 'yaml', yml: 'yaml', json: 'json', xml: 'xml', html: 'html',
    css: 'css', scss: 'scss', toml: 'toml', md: 'markdown', sql: 'sql',
    dockerfile: 'dockerfile',
  }
  return map[ext] || ext
}

function isMarkdown(name: string): boolean {
  return name.endsWith('.md') || name.endsWith('.mdx')
}

function isImage(name: string): boolean {
  const ext = name.split('.').pop()?.toLowerCase() || ''
  return ['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico'].includes(ext)
}

function formatSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

function highlightCode(code: string, lang: string): string {
  if (lang && hljs.getLanguage(lang)) {
    return hljs.highlight(code, { language: lang }).value
  }
  return hljs.highlightAuto(code).value
}

function renderMarkdown(content: string): string {
  return DOMPurify.sanitize(md.render(content))
}

// -- API calls --

async function loadRoot() {
  loading.value = true
  error.value = ''
  try {
    const entries = await apiFetch<FileEntry[]>('/files/list')
    treeRoots.value = entries.map((e) => ({
      entry: e,
      children: null,
      expanded: false,
      loading: false,
      error: null,
    }))
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load files'
  } finally {
    loading.value = false
  }
}

async function toggleDir(node: TreeNode) {
  if (node.expanded) {
    node.expanded = false
    return
  }

  node.expanded = true

  if (node.children !== null) return // already loaded

  node.loading = true
  node.error = null
  try {
    const entries = await apiFetch<FileEntry[]>('/files/list?dir=' + encodeURIComponent(node.entry.path))
    node.children = entries.map((e) => ({
      entry: e,
      children: null,
      expanded: false,
      loading: false,
      error: null,
    }))
  } catch (e: unknown) {
    node.error = e instanceof Error ? e.message : 'Failed to load'
  } finally {
    node.loading = false
  }
}

async function selectFile(path: string) {
  selectedPath.value = path
  fileContent.value = null
  loading.value = true
  error.value = ''
  try {
    fileContent.value = await apiFetch<FileContent>('/files?path=' + encodeURIComponent(path))
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load file'
  } finally {
    loading.value = false
  }
}

function downloadFile(path: string) {
  const url = `/api/files/download?path=${encodeURIComponent(path)}`
  fetch(url)
    .then((res) => {
      if (!res.ok) throw new Error('Download failed')
      return res.blob()
    })
    .then((blob) => {
      const blobUrl = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = blobUrl
      a.download = path.split('/').pop() || 'file'
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(blobUrl)
    })
    .catch(() => {})
}

function copyContent() {
  if (!fileContent.value?.content) return
  navigator.clipboard.writeText(fileContent.value.content).then(() => {
    copied.value = true
    setTimeout(() => (copied.value = false), 2000)
  })
}

const selectedName = computed(() => {
  if (!selectedPath.value) return ''
  return selectedPath.value.split('/').pop() || ''
})

const highlightedContent = computed(() => {
  if (!fileContent.value?.content) return ''
  if (isMarkdown(fileContent.value.name)) {
    return renderMarkdown(fileContent.value.content)
  }
  const lang = getLanguage(fileContent.value.name)
  return highlightCode(fileContent.value.content, lang)
})

// -- Init --
onMounted(loadRoot)
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <h1 class="text-lg font-semibold">Files</h1>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="loadRoot">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        </Button>
      </div>
    </div>

    <!-- Main content: sidebar + viewer -->
    <div class="flex flex-1 overflow-hidden">
      <!-- File tree sidebar -->
      <div class="w-64 shrink-0 border-r border-border bg-sidebar-background">
        <ScrollArea class="h-full">
          <div class="p-2">
            <!-- Loading -->
            <div v-if="loading && treeRoots.length === 0" class="p-4 text-center text-sm text-muted-foreground">
              Loading...
            </div>

            <!-- Error -->
            <div v-else-if="error && treeRoots.length === 0" class="p-4">
              <div class="flex items-center gap-2 text-sm text-destructive">
                <AlertCircle class="h-4 w-4 shrink-0" />
                <span>{{ error }}</span>
              </div>
              <Button variant="ghost" size="sm" class="mt-2" @click="loadRoot">
                Retry
              </Button>
            </div>

            <!-- Tree -->
            <template v-else>
              <div v-if="treeRoots.length === 0" class="p-4 text-center text-sm text-muted-foreground">
                No files found
              </div>
              <TreeItem
                v-for="node in treeRoots"
                :key="node.entry.path"
                :node="node"
                :selected-path="selectedPath"
                @toggle="toggleDir"
                @select="selectFile"
              />
            </template>
          </div>
        </ScrollArea>
      </div>

      <!-- Content viewer -->
      <div class="flex flex-1 flex-col overflow-hidden">
        <!-- No file selected -->
        <div v-if="!selectedPath" class="flex flex-1 items-center justify-center text-muted-foreground">
          <div class="text-center">
            <FileCode class="mx-auto mb-2 h-8 w-8 opacity-50" />
            <p class="text-sm">Select a file to view</p>
          </div>
        </div>

        <!-- File header bar -->
        <div v-else class="flex shrink-0 items-center justify-between border-b border-border px-4 py-2">
          <div class="flex items-center gap-2 overflow-hidden">
            <component :is="getFileIcon(selectedName)" class="h-4 w-4 shrink-0 text-muted-foreground" />
            <span class="truncate text-sm font-medium">{{ selectedName }}</span>
            <Badge v-if="fileContent?.truncated" variant="secondary" class="shrink-0 text-xs">
              Truncated
            </Badge>
            <Badge v-if="fileContent?.is_binary && !isImage(selectedName)" variant="secondary" class="shrink-0 text-xs">
              Binary
            </Badge>
            <span v-if="fileContent" class="shrink-0 text-xs text-muted-foreground">
              {{ formatSize(fileContent.size) }}
            </span>
          </div>
          <div class="flex items-center gap-1">
            <Button
              v-if="fileContent?.content"
              variant="ghost"
              size="sm"
              class="h-7 px-2"
              @click="copyContent"
            >
              <Check v-if="copied" class="h-3.5 w-3.5 text-green-500" />
              <Copy v-else class="h-3.5 w-3.5" />
            </Button>
            <Button variant="ghost" size="sm" class="h-7 px-2" @click="downloadFile(selectedPath)">
              <Download class="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>

        <!-- Content area -->
        <div class="flex-1 overflow-auto">
          <!-- Loading -->
          <div v-if="loading && selectedPath" class="flex h-full items-center justify-center text-muted-foreground">
            <span class="text-sm">Loading...</span>
          </div>

          <!-- Error -->
          <div v-else-if="error && selectedPath" class="flex h-full items-center justify-center">
            <div class="text-center">
              <AlertCircle class="mx-auto mb-2 h-6 w-6 text-destructive" />
              <p class="text-sm text-destructive">{{ error }}</p>
            </div>
          </div>

          <!-- Image preview -->
          <div
            v-else-if="fileContent && isImage(selectedName)"
            class="flex h-full items-center justify-center p-4"
          >
            <img
              :src="`/api/files/download?path=${encodeURIComponent(selectedPath)}`"
              :alt="selectedName"
              class="max-h-full max-w-full object-contain"
              @error="error = 'Failed to load image'"
            />
          </div>

          <!-- Markdown rendered -->
          <div
            v-else-if="fileContent?.content && isMarkdown(selectedName)"
            class="prose prose-invert max-w-none p-6 text-sm"
            v-html="highlightedContent"
          />

          <!-- Code / text -->
          <div v-else-if="fileContent?.content" class="h-full">
            <pre class="overflow-auto p-4 font-mono text-sm leading-relaxed"><code v-html="highlightedContent" /></pre>
          </div>

          <!-- Binary file -->
          <div
            v-else-if="fileContent?.is_binary"
            class="flex h-full items-center justify-center"
          >
            <div class="text-center">
              <File class="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
              <p class="text-sm text-muted-foreground">Binary file ({{ fileContent.mime }})</p>
              <Button variant="outline" size="sm" class="mt-3" @click="downloadFile(selectedPath)">
                <Download class="mr-1.5 h-3.5 w-3.5" />
                Download
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.prose :deep(pre) {
  background: transparent;
  padding: 0;
  margin: 0;
  overflow: visible;
}

.prose :deep(code) {
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace;
  font-size: 0.875rem;
  line-height: 1.5;
}

.prose :deep(pre code) {
  display: block;
  overflow-x: auto;
  padding: 1rem;
  background: oklch(0.145 0 0);
  border-radius: 0.375rem;
}

.prose :deep(h1),
.prose :deep(h2),
.prose :deep(h3) {
  color: oklch(0.985 0 0);
}

.prose :deep(p) {
  color: oklch(0.85 0 0);
}

.prose :deep(a) {
  color: oklch(0.6 0.17 162.48);
}

.prose :deep(code:not(pre code)) {
  background: oklch(0.269 0 0);
  padding: 0.125rem 0.25rem;
  border-radius: 0.25rem;
  font-size: 0.8125rem;
}

.prose :deep(ul),
.prose :deep(ol) {
  color: oklch(0.85 0 0);
}

.prose :deep(blockquote) {
  border-color: oklch(0.269 0 0);
  color: oklch(0.708 0 0);
}

.prose :deep(table) {
  color: oklch(0.85 0 0);
}

.prose :deep(th) {
  background: oklch(0.205 0 0);
}
</style>
