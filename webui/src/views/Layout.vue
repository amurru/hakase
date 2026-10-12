<script setup lang="ts">
import { computed, ref, watch, nextTick, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { useThemeStore } from '@/stores/theme'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import NotificationContainer from '@/components/NotificationContainer.vue'
import ApprovalModal from '@/components/approval/ApprovalModal.vue'
import ClarifyModal from '@/components/clarify/ClarifyModal.vue'
import SidebarNav from '@/components/SidebarNav.vue'
import {
  Sun,
  Moon,
  Menu,
  Gauge,
} from '@lucide/vue'

const route = useRoute()
const appStore = useAppStore()
const themeStore = useThemeStore()

// Section breadcrumb for the header (UX-03).
const sectionLabels: Record<string, string> = {
  chat: 'Chat',
  sessions: 'Sessions',
  projects: 'Projects',
  tasks: 'Tasks',
  knowledge: 'Knowledge',
  memory: 'Memory',
  hooks: 'Hooks',
  skills: 'Skills',
  mcp: 'MCP',
  cron: 'Cron',
  channels: 'Channels',
  files: 'Files',
  settings: 'Settings',
}
const sectionLabel = computed(() => sectionLabels[route.path.replace('/', '')] ?? 'Hakase')

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
      return 'bg-amber-500 motion-safe:animate-pulse'
    case 'disconnected':
      return 'bg-red-500'
  }
})

// Sidebar state (UX-02): collapsible on desktop (persisted), drawer on
// mobile. The drawer closes on Escape.
const COLLAPSED_KEY = 'hakase_sidebar_collapsed'
const collapsed = ref(localStorage.getItem(COLLAPSED_KEY) === '1')
const mobileOpen = ref(false)
// Refs for drawer focus management (open: focus in; close: focus back).
const menuButton = ref<{ $el: HTMLButtonElement } | null>(null)
const drawerAside = ref<HTMLElement | null>(null)

function toggleCollapsed() {
  collapsed.value = !collapsed.value
  localStorage.setItem(COLLAPSED_KEY, collapsed.value ? '1' : '0')
}

function onDrawerKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    mobileOpen.value = false
    return
  }
  // Keep tab cycling inside the modal drawer while it is open.
  if (e.key !== 'Tab' || !drawerAside.value) return
  const items = [...drawerAside.value.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])',
  )]
  if (items.length === 0) return
  const first = items[0]!
  const last = items[items.length - 1]!
  const active = document.activeElement as HTMLElement | null
  if (e.shiftKey && (active === first || !drawerAside.value.contains(active))) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && active === last) {
    e.preventDefault()
    first.focus()
  }
}

watch(mobileOpen, (open) => {
  if (open) {
    document.addEventListener('keydown', onDrawerKeydown)
    // Move focus into the dialog; keyboard users otherwise keep tabbing
    // through the page behind the overlay.
    nextTick(() => {
      drawerAside.value
        ?.querySelector<HTMLElement>('a[href], button:not([disabled])')
        ?.focus()
    })
  } else {
    document.removeEventListener('keydown', onDrawerKeydown)
    menuButton.value?.$el?.focus()
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
      <aside ref="drawerAside" class="absolute inset-y-0 left-0 flex w-72 flex-col border-r border-border bg-sidebar-background text-sidebar-foreground">
        <SidebarNav :show-collapse="false" @navigate="mobileOpen = false" @toggle-collapse="toggleCollapsed" />
      </aside>
    </div>

    <!-- Main area -->
    <div class="flex flex-1 flex-col overflow-hidden">
      <!-- Header -->
      <header class="flex h-14 shrink-0 items-center justify-between border-b border-border px-4">
        <div class="flex min-w-0 items-center gap-3">
          <Button
            ref="menuButton"
            variant="ghost"
            size="sm"
            class="h-8 w-8 shrink-0 p-0 lg:hidden"
            aria-label="Open navigation"
            title="Open navigation"
            @click="mobileOpen = true"
          >
            <Menu class="h-4 w-4" />
          </Button>
          <span class="truncate text-sm font-semibold">{{ sectionLabel }}</span>
          <span class="hidden shrink-0 text-sm text-muted-foreground/50 sm:inline">{{ appStore.modelName }}</span>
          <div class="flex shrink-0 items-center gap-1.5">
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
            <span class="hidden text-xs text-muted-foreground capitalize sm:inline">{{ appStore.connectionStatus }}</span>
          </div>
        </div>

        <div class="flex shrink-0 items-center gap-1">
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
          <!-- Usage popover: one meter in the header, details on demand
               (UX-03). Context/Cache/Cost no longer crowd the bar. -->
          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <Button
                variant="ghost"
                size="sm"
                class="gap-1.5 text-muted-foreground"
                title="Model usage details"
                aria-label="Model usage details"
              >
                <Gauge class="h-4 w-4" />
                <span class="text-xs tabular-nums">{{ contextPercent }}%</span>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" class="w-64 p-3">
              <div class="flex items-center justify-between gap-2">
                <span class="text-xs font-medium text-muted-foreground">Context</span>
                <span class="text-xs tabular-nums text-muted-foreground">{{ contextPercent }}%</span>
              </div>
              <Progress :model-value="contextPercent" class="mt-1.5 h-1.5 w-full" />
              <div v-if="appStore.totalTokens > 0" class="mt-2.5 flex items-center justify-between gap-2">
                <span class="text-xs font-medium text-muted-foreground">Prompt-cache read</span>
                <span class="text-xs tabular-nums text-muted-foreground">{{ cachePct }}%</span>
              </div>
              <div v-if="appStore.costUsd > 0" class="mt-2.5">
                <div class="flex items-center justify-between gap-2">
                  <span class="text-xs font-medium text-muted-foreground">Cost this turn</span>
                  <span class="text-xs tabular-nums text-muted-foreground">${{ appStore.costUsd.toFixed(4) }}</span>
                </div>
                <div v-if="appStore.budgetPct > 0" class="mt-1.5 flex items-center gap-2">
                  <Progress :model-value="appStore.budgetPct" class="h-1.5 flex-1" />
                  <span class="text-xs tabular-nums text-muted-foreground">{{ appStore.budgetPct }}%</span>
                </div>
              </div>
              <p v-if="appStore.totalTokens === 0 && appStore.costUsd === 0" class="mt-2.5 text-[11px] text-muted-foreground/60">
                Token and cost meters appear here once the first turn streams usage.
              </p>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>

      <!-- Content -->
      <main class="flex-1 overflow-auto">
        <RouterView />
      </main>

      <!-- Status bar: token count only. The session title already shows in
           the chat header (and renames there), so repeating it here tripled
           the same string on screen. -->
      <footer class="flex h-8 shrink-0 items-center border-t border-border bg-muted/50 px-4">
        <div class="flex items-center gap-3 text-xs text-muted-foreground">
          <span v-if="appStore.totalTokens > 0" class="tabular-nums">
            {{ appStore.totalTokens.toLocaleString() }} tokens
          </span>
          <span v-else-if="!appStore.activeSessionTitle">No active session</span>
        </div>
      </footer>
    </div>
  </div>
</template>
