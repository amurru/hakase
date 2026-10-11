<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import { useAppStore } from '@/stores/app'
import { useThemeStore } from '@/stores/theme'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import NotificationContainer from '@/components/NotificationContainer.vue'
import ApprovalModal from '@/components/approval/ApprovalModal.vue'
import ClarifyModal from '@/components/clarify/ClarifyModal.vue'
import SidebarNav from '@/components/SidebarNav.vue'
import {
  Loader2,
  Sun,
  Moon,
  Menu,
} from '@lucide/vue'

const appStore = useAppStore()
const themeStore = useThemeStore()

const contextPercent = computed(() => {
  if (appStore.contextMax === 0) return 0
  return Math.round((appStore.contextUsage / appStore.contextMax) * 100)
})

// cachePct is the prompt-cache read share of the turn's total tokens
// (PC-007 SSE field; 0 until a usage event carries cached tokens).
const cachePct = computed(() => {
  if (appStore.totalTokens <= 0) return 0
  return Math.min(100, Math.round((appStore.cachedTokens / appStore.totalTokens) * 100))
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

// Sidebar state (UX-02): collapsible on desktop (persisted), drawer on
// mobile. The drawer closes on Escape.
const COLLAPSED_KEY = 'hakase_sidebar_collapsed'
const collapsed = ref(localStorage.getItem(COLLAPSED_KEY) === '1')
const mobileOpen = ref(false)

function toggleCollapsed() {
  collapsed.value = !collapsed.value
  localStorage.setItem(COLLAPSED_KEY, collapsed.value ? '1' : '0')
}

function onDrawerKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') mobileOpen.value = false
}

watch(mobileOpen, (open) => {
  if (open) {
    document.addEventListener('keydown', onDrawerKeydown)
  } else {
    document.removeEventListener('keydown', onDrawerKeydown)
  }
})

onUnmounted(() => {
  document.removeEventListener('keydown', onDrawerKeydown)
})
</script>

<template>
  <div class="flex h-screen overflow-hidden bg-background text-foreground">
    <!-- Global modals -->
    <NotificationContainer />
    <ClarifyModal />
    <!-- Approval Modal (global overlay) -->
    <ApprovalModal />

    <!-- Sidebar (desktop): grouped nav, collapsible to icons. -->
    <aside
      class="hidden shrink-0 flex-col border-r border-border bg-sidebar-background text-sidebar-foreground transition-[width] lg:flex"
      :class="collapsed ? 'w-16' : 'w-56'"
    >
      <SidebarNav :collapsed="collapsed" @toggle-collapse="toggleCollapsed" />
    </aside>

    <!-- Sidebar (mobile): overlay drawer below lg. -->
    <div v-if="mobileOpen" class="fixed inset-0 z-50 lg:hidden" role="dialog" aria-modal="true" aria-label="Navigation">
      <div class="absolute inset-0 bg-black/60" @click="mobileOpen = false" />
      <aside class="absolute inset-y-0 left-0 flex w-72 flex-col border-r border-border bg-sidebar-background text-sidebar-foreground">
        <SidebarNav @navigate="mobileOpen = false" @toggle-collapse="toggleCollapsed" />
      </aside>
    </div>

    <!-- Main area -->
    <div class="flex flex-1 flex-col overflow-hidden">
      <!-- Header -->
      <header class="flex h-14 shrink-0 items-center justify-between border-b border-border px-4">
        <div class="flex items-center gap-3">
          <Button
            variant="ghost"
            size="sm"
            class="h-8 w-8 p-0 lg:hidden"
            aria-label="Open navigation"
            title="Open navigation"
            @click="mobileOpen = true"
          >
            <Menu class="h-4 w-4" />
          </Button>
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
          <!-- Prompt-cache read (PC-007; hidden until a usage event lands) -->
          <div v-if="appStore.totalTokens > 0" class="flex items-center gap-2">
            <span class="text-xs text-muted-foreground whitespace-nowrap">Cache</span>
            <span class="text-xs text-muted-foreground tabular-nums">{{ cachePct }}%</span>
          </div>
          <!-- Cost meter (FinOps SSE v2; hidden until the first usage event) -->
          <div v-if="appStore.costUsd > 0" class="flex items-center gap-2">
            <span class="text-xs text-muted-foreground whitespace-nowrap tabular-nums">${{ appStore.costUsd.toFixed(4) }}</span>
            <Progress v-if="appStore.budgetPct > 0" :model-value="appStore.budgetPct" class="h-1.5 w-16" />
            <span v-if="appStore.budgetPct > 0" class="text-xs text-muted-foreground tabular-nums">{{ appStore.budgetPct }}%</span>
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
