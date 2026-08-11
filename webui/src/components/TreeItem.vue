<script setup lang="ts">
import {
  FolderOpen,
  Folder,
  ChevronRight,
  ChevronDown,
  FileText,
  FileCode,
  FileImage,
  File,
} from '@lucide/vue'

interface TreeNode {
  entry: { name: string; path: string; is_dir: boolean; size: number }
  children: TreeNode[] | null
  expanded: boolean
  loading: boolean
  error: string | null
}

const props = defineProps<{
  node: TreeNode
  selectedPath: string
}>()

const emit = defineEmits<{
  toggle: [node: TreeNode]
  select: [path: string]
}>()

function getFileIcon(name: string, isDir: boolean) {
  if (isDir) return Folder
  const ext = name.split('.').pop()?.toLowerCase() || ''
  if (['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico'].includes(ext)) return FileImage
  if (['js', 'ts', 'tsx', 'jsx', 'vue', 'go', 'py', 'rs', 'rb', 'java', 'c', 'cpp', 'h', 'sh'].includes(ext))
    return FileCode
  if (['md', 'txt', 'rst', 'log'].includes(ext)) return FileText
  return File
}

function handleClick() {
  if (props.node.entry.is_dir) {
    emit('toggle', props.node)
  } else {
    emit('select', props.node.entry.path)
  }
}
</script>

<template>
  <div>
    <!-- Item row -->
    <div
      class="flex cursor-pointer items-center gap-1.5 rounded-md px-2 py-1 text-sm transition-colors hover:bg-accent"
      :class="{ 'bg-accent': selectedPath === node.entry.path }"
      @click="handleClick"
    >
      <!-- Chevron -->
      <ChevronDown
        v-if="node.entry.is_dir && node.expanded"
        class="h-3 w-3 shrink-0 text-muted-foreground"
      />
      <ChevronRight
        v-else-if="node.entry.is_dir"
        class="h-3 w-3 shrink-0 text-muted-foreground"
      />
      <span v-else class="w-3 shrink-0" />

      <!-- Icon -->
      <component
        :is="node.entry.is_dir ? (node.expanded ? FolderOpen : Folder) : getFileIcon(node.entry.name, false)"
        class="h-3.5 w-3.5 shrink-0 text-muted-foreground"
      />

      <!-- Name -->
      <span
        class="truncate text-[13px]"
        :class="selectedPath === node.entry.path ? 'font-medium text-foreground' : 'text-muted-foreground'"
      >
        {{ node.entry.name }}
      </span>
    </div>

    <!-- Children -->
    <div v-if="node.expanded" class="pl-3">
      <div v-if="node.loading" class="px-2 py-1 text-xs text-muted-foreground">Loading...</div>
      <div v-else-if="node.error" class="px-2 py-1 text-xs text-destructive">{{ node.error }}</div>
      <template v-else-if="node.children">
        <TreeItem
          v-for="child in node.children"
          :key="child.entry.path"
          :node="child"
          :selected-path="selectedPath"
          @toggle="(n: TreeNode) => $emit('toggle', n)"
          @select="(p: string) => $emit('select', p)"
        />
      </template>
    </div>
  </div>
</template>
