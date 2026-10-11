<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  MessageSquare,
  FolderOpen,
  FolderGit2,
  ListTodo,
  BookOpen,
  Brain,
  PlugZap,
  Wand2,
  Puzzle,
  Clock,
  Files,
  Send,
  Settings,
  LogOut,
  Bot,
  PanelLeftClose,
  PanelLeftOpen,
} from '@lucide/vue'

defineProps<{
  collapsed?: boolean
}>()

const emit = defineEmits<{
  navigate: []
  toggleCollapse: []
}>()

const route = useRoute()
const authStore = useAuthStore()

// Same 13 destinations, grouped by workflow (UX-02). Routes unchanged.
const groups = [
  {
    label: 'Work',
    items: [
      { to: '/chat', label: 'Chat', icon: MessageSquare },
      { to: '/sessions', label: 'Sessions', icon: FolderOpen },
      { to: '/tasks', label: 'Tasks', icon: ListTodo },
    ],
  },
  {
    label: 'Context',
    items: [
      { to: '/projects', label: 'Projects', icon: FolderGit2 },
      { to: '/knowledge', label: 'Knowledge', icon: BookOpen },
      { to: '/memory', label: 'Memory', icon: Brain },
      { to: '/files', label: 'Files', icon: Files },
    ],
  },
  {
    label: 'Extend',
    items: [
      { to: '/hooks', label: 'Hooks', icon: PlugZap },
      { to: '/skills', label: 'Skills', icon: Wand2 },
      { to: '/mcp', label: 'MCP', icon: Puzzle },
      { to: '/cron', label: 'Cron', icon: Clock },
      { to: '/channels', label: 'Channels', icon: Send },
    ],
  },
  {
    label: 'System',
    items: [{ to: '/settings', label: 'Settings', icon: Settings }],
  },
]

function isActive(path: string) {
  return route.path === path
}

function handleLogout() {
  authStore.logout()
  window.location.href = '/login'
}
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Logo -->
    <div class="flex h-14 shrink-0 items-center gap-2 px-4" :class="collapsed && 'justify-center px-0'">
      <Bot class="h-6 w-6 shrink-0 text-primary" />
      <span v-if="!collapsed" class="text-lg font-semibold tracking-tight">Hakase</span>
    </div>

    <Separator />

    <!-- Navigation -->
    <ScrollArea class="flex-1 py-2">
      <nav
        v-for="group in groups"
        :key="group.label"
        class="flex flex-col gap-1 px-2"
        :class="collapsed ? 'py-1' : 'pb-3'"
        :aria-label="group.label"
      >
        <p
          v-if="!collapsed"
          class="px-3 pt-2 pb-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/60"
        >
          {{ group.label }}
        </p>
        <RouterLink
          v-for="item in group.items"
          :key="item.to"
          :to="item.to"
          :title="collapsed ? item.label : undefined"
          class="flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
          :class="[
            collapsed && 'justify-center px-0',
            isActive(item.to)
              ? 'bg-sidebar-accent text-sidebar-accent-foreground'
              : 'text-sidebar-foreground/70',
          ]"
          @click="emit('navigate')"
        >
          <component :is="item.icon" class="h-4 w-4 shrink-0" />
          <span v-if="!collapsed">{{ item.label }}</span>
        </RouterLink>
      </nav>
    </ScrollArea>

    <!-- Footer -->
    <div
      class="border-t border-sidebar-border p-3"
      :class="collapsed ? 'flex flex-col items-center gap-1' : 'flex flex-col gap-1'"
    >
      <Button
        variant="ghost"
        size="sm"
        :title="collapsed ? 'Expand sidebar' : 'Collapse sidebar'"
        :aria-label="collapsed ? 'Expand sidebar' : 'Collapse sidebar'"
        :class="collapsed ? 'h-8 w-8 p-0' : 'w-full justify-start gap-2'"
        @click="emit('toggleCollapse')"
      >
        <PanelLeftOpen v-if="collapsed" class="h-4 w-4" />
        <PanelLeftClose v-else class="h-4 w-4" />
        <span v-if="!collapsed" class="text-sidebar-foreground/70">Collapse</span>
      </Button>
      <Button
        variant="ghost"
        size="sm"
        title="Log out"
        aria-label="Log out"
        :class="collapsed
          ? 'h-8 w-8 p-0 text-sidebar-foreground/70 hover:text-sidebar-foreground'
          : 'w-full justify-start gap-2 text-sidebar-foreground/70 hover:text-sidebar-foreground'"
        @click="handleLogout"
      >
        <LogOut class="h-4 w-4" />
        <span v-if="!collapsed">Log out</span>
      </Button>
    </div>
  </div>
</template>
