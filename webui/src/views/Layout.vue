<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import NotificationContainer from '@/components/NotificationContainer.vue'
import ApprovalModal from '@/components/approval/ApprovalModal.vue'
import ClarifyModal from '@/components/clarify/ClarifyModal.vue'
import {
  MessageSquare,
  FolderOpen,
  ListTodo,
  BookOpen,
  Wand2,
  Puzzle,
  Clock,
  Files,
  Settings,
  LogOut,
  Bot,
  Loader2,
  Sun,
  Moon,
} from '@lucide/vue'

const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()
const themeStore = useThemeStore()

const contextPercent = computed(() => {
  if (appStore.contextMax === 0) return 0
  return Math.round((appStore.contextUsage / appStore.contextMax) * 100)
})

const statusColor = computed(() => {
  switch (appStore.connectionStatus) {
    case 'connected':
      return 'bg-emerald-500'
    case 'connecting':
      return 'bg-amber-500 animate-pulse'
    case 'disconnected':
      return 'bg-red-500'
  }
})

const navItems = [
  { to: '/chat', label: 'Chat', icon: MessageSquare },
  { to: '/sessions', label: 'Sessions', icon: FolderOpen },
  { to: '/tasks', label: 'Tasks', icon: ListTodo },
  { to: '/knowledge', label: 'Knowledge', icon: BookOpen },
  { to: '/skills', label: 'Skills', icon: Wand2 },
  { to: '/mcp', label: 'MCP', icon: Puzzle },
  { to: '/cron', label: 'Cron', icon: Clock },
  { to: '/files', label: 'Files', icon: Files },
  { to: '/settings', label: 'Settings', icon: Settings },
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
  <div class="flex h-screen overflow-hidden bg-background text-foreground">
    <!-- Global modals -->
    <NotificationContainer />
    <ClarifyModal />
    <!-- Approval Modal (global overlay) -->
    <ApprovalModal />

    <!-- Sidebar -->
    <aside class="flex w-56 flex-col border-r border-border bg-sidebar-background text-sidebar-foreground">
      <!-- Logo -->
      <div class="flex h-14 items-center gap-2 px-4">
        <Bot class="h-6 w-6 text-primary" />
        <span class="text-lg font-semibold tracking-tight">Hakase</span>
      </div>

      <Separator />

      <!-- Navigation -->
      <ScrollArea class="flex-1 py-2">
        <nav class="flex flex-col gap-1 px-2">
          <RouterLink
            v-for="item in navItems"
            :key="item.to"
            :to="item.to"
            class="flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
            :class="[
              isActive(item.to)
                ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                : 'text-sidebar-foreground/70',
            ]"
          >
            <component :is="item.icon" class="h-4 w-4 shrink-0" />
            {{ item.label }}
          </RouterLink>
        </nav>
      </ScrollArea>

      <!-- Sidebar footer -->
      <div class="border-t border-sidebar-border p-3">
        <Button
          variant="ghost"
          size="sm"
          class="w-full justify-start gap-2 text-sidebar-foreground/70 hover:text-sidebar-foreground"
          @click="handleLogout"
        >
          <LogOut class="h-4 w-4" />
          <span>Log out</span>
        </Button>
      </div>
    </aside>

    <!-- Main area -->
    <div class="flex flex-1 flex-col overflow-hidden">
      <!-- Header -->
      <header class="flex h-14 shrink-0 items-center justify-between border-b border-border px-4">
        <div class="flex items-center gap-3">
          <span class="text-sm font-medium">{{ appStore.modelName }}</span>
          <div class="flex items-center gap-2">
            <div class="flex items-center gap-1.5">
              <span class="relative flex h-2 w-2">
                <span
                  class="absolute inline-flex h-full w-full rounded-full opacity-75"
                  :class="[statusColor]"
                />
                <span
                  class="relative inline-flex h-2 w-2 rounded-full"
                  :class="[statusColor]"
                />
              </span>
              <span class="text-xs text-muted-foreground capitalize">{{ appStore.connectionStatus }}</span>
            </div>
          </div>
        </div>

        <div class="flex items-center gap-4">
          <!-- Theme toggle -->
          <Button
            variant="ghost"
            size="sm"
            class="h-8 w-8 p-0"
            :aria-label="themeStore.theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'"
            :title="themeStore.theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'"
            @click="themeStore.toggleTheme"
          >
            <Sun v-if="themeStore.theme === 'dark'" class="h-4 w-4" />
            <Moon v-else class="h-4 w-4" />
          </Button>
          <!-- Context usage -->
          <div class="flex items-center gap-2">
            <span class="text-xs text-muted-foreground whitespace-nowrap">Context</span>
            <Progress :model-value="contextPercent" class="h-1.5 w-24" />
            <span class="text-xs text-muted-foreground tabular-nums">{{ contextPercent }}%</span>
          </div>
        </div>
      </header>

      <!-- Content -->
      <main class="flex-1 overflow-auto">
        <RouterView />
      </main>

      <!-- Status bar -->
      <footer class="flex h-8 shrink-0 items-center justify-between border-t border-border bg-muted/50 px-4">
        <div class="flex items-center gap-3 text-xs text-muted-foreground">
          <span v-if="appStore.activeSessionTitle" class="max-w-64 truncate">
            {{ appStore.activeSessionTitle }}
          </span>
          <span v-else>No active session</span>
          <span v-if="appStore.totalTokens > 0" class="tabular-nums">
            {{ appStore.totalTokens.toLocaleString() }} tokens
          </span>
        </div>
        <div class="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            class="h-6 px-2 text-xs"
            :class="appStore.thinkingEnabled ? 'text-primary' : 'text-muted-foreground'"
            @click="appStore.toggleThinking"
          >
            <Loader2 v-if="appStore.thinkingEnabled" class="mr-1 h-3 w-3 animate-spin" />
            Thinking
          </Button>
        </div>
      </footer>
    </div>
  </div>
</template>
