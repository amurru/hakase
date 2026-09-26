<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted, computed, defineAsyncComponent } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { useSessionStore } from '@/stores/session'
import { useApprovalStore } from '@/stores/approval'
import { useClarifyStore } from '@/stores/clarify'
import { useCanvasStore } from '@/stores/canvas'
import { useSSE, type ChatMessage } from '@/composables/useSSE'
import { sidekickSeverityClass, type SidekickNote } from '@/lib/sidekick'
import { parseSlashCommand, SLASH_COMMANDS } from '@/lib/slash'
import {
  activeTickIndex,
  layoutRail,
  railPreviewText,
  shouldShowRail,
  type RailAnchor,
  type RailTickVM,
} from '@/lib/messageRail'
import { useResizeObserver } from '@vueuse/core'
import { useNotifications } from '@/composables/useNotifications'
import { apiFetch, listSnapshots, restoreSession, type SessionSnapshot } from '@/lib/api'
import { useProjectsStore, type ProjectStatus } from '@/stores/projects'
import MessageBubble from '@/components/chat/MessageBubble.vue'
import MessageRail from '@/components/chat/MessageRail.vue'
import ChatInput from '@/components/chat/ChatInput.vue'
import type { FileAttachment } from '@/components/chat/AttachmentPicker.vue'
import { AlertTriangle, Loader2, Info, AlertCircle, Lightbulb, GitBranch, Check, Workflow } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

// The canvas pulls in vue-flow (~large); keep it out of the main chunk like
// the mermaid renderer.
const ExecutionCanvas = defineAsyncComponent(() => import('@/components/canvas/ExecutionCanvas.vue'))

const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const sessionStore = useSessionStore()
const approvalStore = useApprovalStore()
const clarifyStore = useClarifyStore()
const projectsStore = useProjectsStore()
const canvasStore = useCanvasStore()

// Live branch + dirty indicator for the bound-project chip (project-ui.md).
// Read without a fetch: the header must not force network I/O on every open.
const projectBranch = ref('')
const projectDirty = ref(false)
const projectDirtyDetail = ref('')

const sessionId = ref<string | null>(null)
const isLoadingHistory = ref(false)
const scrollContainer = ref<HTMLDivElement | null>(null)
const isUserScrolledUp = ref(false)
// Message rail: one tick per user prompt + the prompt being read.
const railTicks = ref<RailTickVM[]>([])
const activeTickId = ref<string | null>(null)

// Initialize SSE composable
const {
  messages,
  sidekickNotes,
  isStreaming,
  connected,
  sendMessage,
  connect,
  onApprovalEvent,
  onApprovalTimeoutEvent,
  onClarifyEvent,
  onClarifyTimeoutEvent,
  onDelegationEvent,
  onCronEvent,
  onLogEvent,
  onGraphEvent,
  askSidekick,
  pushSidekickNote,
  clearMessages,
} = useSSE(() => sessionId.value)

// Wire SSE approval/clarify events to their Pinia stores
onApprovalEvent((data) => approvalStore.handleApprovalEvent(data))
onApprovalTimeoutEvent((id) => approvalStore.handleApprovalTimeout(id))
onClarifyEvent((data) => clarifyStore.handleClarifyEvent(data))
onClarifyTimeoutEvent((data) => clarifyStore.handleClarifyTimeout(data))

// Wire notification handlers for delegation and cron SSE events
const { handleDelegation, handleCron } = useNotifications()
onDelegationEvent(handleDelegation)
onCronEvent(handleCron)

// Execution canvas: structured graph events feed the Pinia canvas store; a
// session switch rebuilds the graph from the server's retained ring first.
onGraphEvent((data) => canvasStore.handleGraphEvent(data))
watch(
  sessionId,
  (sid) => canvasStore.reset(sid),
  { immediate: true },
)

// Surface agent-run errors: runAgentTask emits failures as SSE log events
// prefixed "Error:" (loop-guard aborts, provider errors). Without this
// handler they reached the browser and were silently discarded, making a
// dead run look like a hung one. Tool Call/Response log lines stay silent -
// they are pane chatter, not problems.
onLogEvent((line) => {
  if (typeof line === 'string' && line.startsWith('Error:')) {
    note('warning', line)
  }
})

// Map a sidekick severity to its quiet chip icon (no notification).
const severityIcons: Record<string, unknown> = {
  info: Info,
  suggestion: Lightbulb,
  warning: AlertTriangle,
  critical: AlertCircle,
  error: AlertCircle,
}
function sidekickIcon(severity: string) {
  return (severityIcons[severity] ?? Info) as unknown
}

// dirtyDescription renders the per-category counts behind the chip's dirty dot.
function dirtyDescription(st: ProjectStatus): string {
  const parts: string[] = []
  if (st.staged > 0) parts.push(`${st.staged} staged`)
  if (st.modified > 0) parts.push(`${st.modified} modified`)
  if (st.untracked > 0) parts.push(`${st.untracked} untracked`)
  if (st.conflicts > 0) parts.push(`${st.conflicts} conflicts`)
  return parts.join(', ')
}

// refreshProjectState extends the bound-project chip with the checkout's live
// branch and a dirty dot (project-ui.md). No server fetch runs: the status
// read is local-only. Unknown/not-ready projects leave the name-only chip.
async function refreshProjectState(id: string | null | undefined) {
  projectBranch.value = ''
  projectDirty.value = false
  projectDirtyDetail.value = ''
  if (!id) return
  const st = await projectsStore.loadStatus(id, { fetch: false })
  if (!st || st.project_status !== 'ready') return
  projectBranch.value = st.branch ?? ''
  projectDirty.value = st.dirty
  if (st.dirty) {
    projectDirtyDetail.value = dirtyDescription(st)
  }
}

// Context usage warning (>= 80%)
const contextWarning = computed(() => {
  if (appStore.contextMax === 0) return false
  const pct = (appStore.contextUsage / appStore.contextMax) * 100
  return pct >= 80
})

const contextPct = computed(() => {
  if (appStore.contextMax === 0) return 0
  return Math.round((appStore.contextUsage / appStore.contextMax) * 100)
})

// --- Inline session-title rename (double-click the header title) ---
const isRenamingTitle = ref(false)
const renameTitleValue = ref('')
const titleInput = ref<{ $el: HTMLInputElement } | null>(null)
const titleEditWrap = ref<HTMLElement | null>(null)

function startTitleRename() {
  // Nothing to rename before a session exists (lazy-created on first message).
  if (!sessionId.value || isRenamingTitle.value) return
  renameTitleValue.value = appStore.activeSessionTitle || 'New Session'
  isRenamingTitle.value = true
  nextTick(() => {
    const el = titleInput.value?.$el
    el?.focus()
    el?.select()
  })
}

function cancelTitleRename() {
  isRenamingTitle.value = false
}

async function commitTitleRename() {
  const sid = sessionId.value
  const title = renameTitleValue.value.trim()
  isRenamingTitle.value = false
  if (!sid || !title || title === appStore.activeSessionTitle) return
  const ok = await sessionStore.renameSession(sid, title)
  if (ok) {
    appStore.setActiveSessionTitle(title)
  } else {
    note('warning', 'rename failed')
  }
}

// Clicking anywhere outside the input+confirm region cancels the rename
// (mousedown so it fires before whatever was clicked processes the click).
function onTitleEditMouseDown(e: MouseEvent) {
  if (titleEditWrap.value && !titleEditWrap.value.contains(e.target as Node)) {
    cancelTitleRename()
  }
}

watch(isRenamingTitle, (editing) => {
  if (editing) {
    document.addEventListener('mousedown', onTitleEditMouseDown)
  } else {
    document.removeEventListener('mousedown', onTitleEditMouseDown)
  }
})

onUnmounted(() => {
  document.removeEventListener('mousedown', onTitleEditMouseDown)
})

// Auto-scroll logic
function scrollToBottom() {
  nextTick(() => {
    const el = scrollContainer.value
    if (el) {
      el.scrollTop = el.scrollHeight
    }
  })
}

function handleScroll() {
  const el = scrollContainer.value
  if (!el) return
  const threshold = 100
  isUserScrolledUp.value = el.scrollHeight - el.scrollTop - el.clientHeight > threshold
  scheduleActiveTick()
}

// Message navigation rail: scroll-offset proportional ticks over the user's
// prompts. Rect reads only happen on content/size changes (one rAF-coalesced
// relayout); scroll events just refresh the active tick from anchor tops
// cached on the ticks.
let railLayoutRaf = 0
let railActiveRaf = 0

function scheduleRailLayout() {
  if (railLayoutRaf) return
  railLayoutRaf = requestAnimationFrame(() => {
    railLayoutRaf = 0
    relayoutRail()
  })
}

function scheduleActiveTick() {
  if (railActiveRaf) return
  railActiveRaf = requestAnimationFrame(() => {
    railActiveRaf = 0
    updateActiveTick()
  })
}

function relayoutRail() {
  const el = scrollContainer.value
  if (!el) return
  const metrics = {
    scrollTop: el.scrollTop,
    clientHeight: el.clientHeight,
    scrollHeight: el.scrollHeight,
  }
  // One querySelectorAll pass; measuring against the container's content top
  // via rects works regardless of the bubbles' offsetParent chain.
  const nodes = el.querySelectorAll<HTMLElement>('[data-message-id]')
  const byId = new Map<string, HTMLElement>()
  for (const node of nodes) {
    if (node.dataset.messageId) byId.set(node.dataset.messageId, node)
  }
  const contentTop = el.getBoundingClientRect().top - el.scrollTop
  const anchors: RailAnchor[] = []
  for (const msg of messages.value) {
    if (msg.role !== 'user') continue
    const node = byId.get(msg.id)
    if (!node) continue
    const rect = node.getBoundingClientRect()
    anchors.push({ id: msg.id, anchorTop: rect.top - contentTop, anchorHeight: rect.height })
  }
  if (!shouldShowRail(anchors.length, metrics)) {
    railTicks.value = []
    activeTickId.value = null
    return
  }
  const byMsg = new Map(messages.value.map((m) => [m.id, m]))
  railTicks.value = layoutRail(anchors, metrics, el.clientHeight).map((tick) => {
    const msg = byMsg.get(tick.id)
    const preview =
      railPreviewText(msg?.content ?? '') ||
      msg?.attachments?.map((a) => a.name).join(', ') ||
      'Attachment'
    return { ...tick, preview, timestamp: msg?.timestamp ?? 0 }
  })
  updateActiveTick()
}

function updateActiveTick() {
  const el = scrollContainer.value
  if (!el || railTicks.value.length === 0) {
    activeTickId.value = null
    return
  }
  const i = activeTickIndex(railTicks.value, {
    scrollTop: el.scrollTop,
    clientHeight: el.clientHeight,
    scrollHeight: el.scrollHeight,
  })
  activeTickId.value = i >= 0 ? railTicks.value[i].id : null
}

function jumpToMessage(id: string) {
  const el = scrollContainer.value
  const tick = railTicks.value.find((t) => t.id === id)
  if (!el || !tick) return
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  el.scrollTo({ top: tick.scrollTarget, behavior: reduceMotion ? 'auto' : 'smooth' })
}

// Canvas panel toggle and window resizes reflow the transcript.
useResizeObserver(scrollContainer, () => scheduleRailLayout())

// Sidekick notes render inside the scroll container: appending one grows
// scrollHeight without touching messages or the container's box, which
// would otherwise leave the rail's visibility and targets stale.
watch(
  () => sidekickNotes.value.length,
  () => scheduleRailLayout(),
)

onUnmounted(() => {
  if (railLayoutRaf) cancelAnimationFrame(railLayoutRaf)
  if (railActiveRaf) cancelAnimationFrame(railActiveRaf)
})

// Scroll to bottom when new messages arrive (if not scrolled up)
watch(
  () => messages.value.length,
  () => {
    scheduleRailLayout()
    if (!isUserScrolledUp.value) {
      scrollToBottom()
    }
  },
)

// Scroll to bottom when streaming content grows; the transcript also gets
// taller, so the rail fractions need a remap.
watch(
  () => messages.value[messages.value.length - 1]?.content,
  () => {
    scheduleRailLayout()
    if (!isUserScrolledUp.value) {
      scrollToBottom()
    }
  },
)

// Load session history
async function loadSessionHistory(sid: string) {
  isLoadingHistory.value = true
  try {
    const data = await apiFetch<{
      title?: string
      project_id?: string
      project_name?: string
      messages?: Array<{
        role: string
        content: string
        thinking?: string
        kind?: string
        timestamp?: string
        sequence?: number
      }>
    }>(
      `/sessions/${sid}`,
    )
    if (data.title) {
      appStore.setActiveSessionTitle(data.title)
    }
    // Project-bound sessions show their registered project as a header chip.
    appStore.setActiveProjectName(data.project_name ?? '')
    // Extend the chip with the checkout's live branch + dirty dot.
    refreshProjectState(data.project_id)
    if (data.messages) {
      for (const msg of data.messages) {
        // Persisted sidekick answers carry role "sidekick" + kind "sidekick";
        // render them as quiet chips, not chat bubbles. Sidekick questions
        // are tagged user turns and stay in the normal transcript.
        if (msg.kind === 'sidekick' && msg.role !== 'user') {
          const note: SidekickNote = {
            id: `hist-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
            severity: 'info',
            text: msg.content,
            timestamp: Date.now(),
          }
          pushSidekickNote(note)
          continue
        }
        messages.value.push({
          id: `hist-${Date.now()}-${Math.random()}`,
          role: msg.role === 'user' ? 'user' : 'agent',
          content: msg.content,
          thinking: msg.thinking ?? '',
          // Server timestamps are RFC3339; keep them so rail tooltips show
          // real times instead of the load moment.
          timestamp: (msg.timestamp && Date.parse(msg.timestamp)) || Date.now(),
          // Server-side transcript position: the restore dialog aligns a
          // message with the snapshot taken just before it.
          sequence: msg.sequence,
        })
      }
      scrollToBottom()
    }
  } catch {
    // Session might not exist yet - that's fine
  } finally {
    isLoadingHistory.value = false
  }
}

// Handle send message
async function handleSend(content: string, fileAttachments?: FileAttachment[]) {
  // Known slash commands route locally - never to the main model. Unknown
  // "/xyz" tokens fall through as ordinary text (they may be paths).
  // Parse before session creation so /new and /compact avoid an unnecessary
  // round-trip to the server.
  const cmd = parseSlashCommand(content)
  if (cmd) {
    // /new and /compact may need a session, but /sidekick does not create one
    // if one doesn't exist yet. Commands that need a session but don't have
    // one will create it lazily inside executeSlash.
    if (!sessionId.value && cmd.name !== 'new' && cmd.name !== 'compact') {
      // Commands like /help that don't need a session at all
      await executeSlash(cmd.name, cmd.args)
      return
    }
    messages.value.push({
      id: `user-${Date.now()}`,
      role: 'user',
      content,
      thinking: '',
      timestamp: Date.now(),
    })
    await executeSlash(cmd.name, cmd.args)
    return
  }

  // Lazily create a session on the first message so there is a place to
  // persist the conversation and an SSE stream to attach to. Without an id,
  // useSSE.sendMessage bails out silently and the message is dropped.
  if (!sessionId.value) {
    const title = content.trim().slice(0, 60) || 'New Session'
    const created = await sessionStore.createSession(title)
    if (!created) {
      console.warn('chat: failed to create session, message not sent')
      return
    }
    sessionId.value = created.id
    appStore.setActiveSessionTitle(created.title || title)
    appStore.setActiveProjectName(created.project_name ?? '')
    refreshProjectState(created.project_id)
    // Keep the URL in sync so a refresh resumes this session. We are already
    // on /chat, so this is a query-only navigation - no remount, no reload.
    router.replace({ path: '/chat', query: { session: created.id } })
  }

  await sendMessage(
    content,
    fileAttachments?.map((a) => ({
      name: a.name,
      path: a.path,
      mime: a.mime,
      label: a.label,
      data: a.data,
    })),
  )
  // SSE connection will be started by useSSE.sendMessage
}

// executeSlash dispatches a recognized slash command. Navigation commands
// reuse the SPA views; stateful ones act through the API. Feedback uses quiet
// chips - never toasts.
async function executeSlash(name: string, args: string) {
  switch (name) {
    case 'sidekick':
      // askSidekick shows a usage chip for empty args and persists nothing
      // client-side; the backend records both turns under kind "sidekick".
      await askSidekick(args)
      return
    case 'compact':
      await runCompact(args)
      return
    case 'new':
      await startNewSession()
      return
    case 'sessions':
      router.push('/sessions')
      return
    case 'board':
      router.push('/tasks')
      return
    case 'mcp':
      router.push('/mcp')
      return
    case 'help': {
      const lines = SLASH_COMMANDS.map(
        (c) => `- \`/${c.name}${c.args ? ` ${c.args}` : ''}\` - ${c.description}`,
      ).join('\n')
      messages.value.push({
        id: `help-${Date.now()}`,
        role: 'agent',
        content: `**Available commands**\n\n${lines}`,
        thinking: '',
        timestamp: Date.now(),
      })
      scrollToBottom()
      return
    }
  }
}

function note(severity: string, text: string) {
  pushSidekickNote({ id: `cmd-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`, severity, text, timestamp: Date.now() })
}

async function startNewSession() {
  const created = await sessionStore.createSession('New Session')
  if (!created) {
    note('warning', 'could not create a new session')
    return
  }
  clearMessages()
  sessionId.value = created.id
  appStore.setActiveSessionTitle(created.title || 'New Session')
  appStore.setActiveProjectName(created.project_name ?? '')
  refreshProjectState(created.project_id)
  router.replace({ path: '/chat', query: { session: created.id } })
}

async function runCompact(focus: string) {
  if (!sessionId.value) {
    note('info', 'nothing to compact yet')
    return
  }
  try {
    await apiFetch(`/sessions/${sessionId.value}/compact`, {
      method: 'POST',
      body: focus ? { focus } : {},
    })
    note('info', 'context compacted; summary generating in background')
    // Reload the session history (which includes the compaction note) before
    // clearing the in-memory message list so the note persists in the view.
    clearMessages()
    await loadSessionHistory(sessionId.value)
  } catch (err) {
    const msg = err instanceof Error ? err.message : 'compact failed'
    note('warning', msg)
  }
}

// --- Restore-to-here (docs/session-rewind/spec.md, issue #21) ---
// The snapshot taken just before a user prompt rewinds the conversation to
// the state before it. Preselection matches the SERVER transcript position
// (message.sequence — client array indexes drift when sidekick records are
// dropped from the rail); when unknown, nothing is preselected and the user
// picks from the list.
const restoreDialogOpen = ref(false)
const restoreBusy = ref(false)
const snapshots = ref<SessionSnapshot[]>([])
const selectedSnapshot = ref<string>('')

async function openRestoreDialog(message: ChatMessage) {
  if (!sessionId.value) return
  restoreDialogOpen.value = true
  try {
    const data = await listSnapshots(sessionId.value)
    snapshots.value = data.snapshots ?? []
    const match =
      message.sequence !== undefined
        ? snapshots.value.find((s) => s.trigger === 'pre' && s.messages === message.sequence)
        : undefined
    selectedSnapshot.value = match?.name ?? ''
  } catch (err) {
    note('warning', err instanceof Error ? err.message : 'failed to list snapshots')
    restoreDialogOpen.value = false
  }
}

async function confirmRestore() {
  if (!sessionId.value || !selectedSnapshot.value || restoreBusy.value) return
  restoreBusy.value = true
  try {
    await restoreSession(sessionId.value, selectedSnapshot.value)
    restoreDialogOpen.value = false
    note('info', 'conversation restored')
    clearMessages()
    await loadSessionHistory(sessionId.value)
  } catch (err) {
    note('warning', err instanceof Error ? err.message : 'restore failed')
  } finally {
    restoreBusy.value = false
  }
}

// Initialize on mount
onMounted(() => {
  // Get session ID from route query or create new session
  const sid = route.query.session as string | undefined
  if (sid) {
    sessionId.value = sid
    appStore.setActiveSessionTitle(`Session ${sid.slice(0, 8)}`)
    loadSessionHistory(sid)
    connect(sid)
  } else {
    // Create a new session on first message (lazy - no session ID yet)
    appStore.setActiveSessionTitle('New Session')
    refreshProjectState(null)
  }
})
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header with session info + context warning -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-4 py-2">
      <div class="flex items-center gap-3">
        <!-- Title: double-click to rename inline. Enter or the check button
             accepts; clicking outside the edit region cancels. -->
        <div
          v-if="isRenamingTitle"
          ref="titleEditWrap"
          class="flex items-center gap-1"
          @dblclick.stop
        >
          <Input
            ref="titleInput"
            v-model="renameTitleValue"
            class="h-7 w-56 text-sm"
            @keydown.enter.prevent="commitTitleRename"
            @keydown.escape.prevent="cancelTitleRename"
          />
          <Button
            variant="ghost"
            size="icon-xs"
            class="text-emerald-500 hover:text-emerald-400"
            title="Accept new name"
            aria-label="Accept new name"
            @click="commitTitleRename"
          >
            <Check class="h-4 w-4" />
          </Button>
        </div>
        <span
          v-else
          class="cursor-text text-sm font-medium text-foreground"
          title="Double-click to rename"
          @dblclick="startTitleRename"
        >
          {{ appStore.activeSessionTitle || 'New Session' }}
        </span>
        <Badge
          v-if="appStore.activeProjectName"
          variant="secondary"
          class="gap-1 text-xs"
          :title="projectDirtyDetail
            ? `Session bound to ${appStore.activeProjectName} - working tree has changes: ${projectDirtyDetail}`
            : `Session bound to registered project ${appStore.activeProjectName}`"
        >
          <GitBranch class="h-3 w-3" />
          {{ appStore.activeProjectName }}
          <span v-if="projectBranch" class="font-mono text-[10px] text-muted-foreground/80">
            {{ projectBranch }}
          </span>
          <span
            v-if="projectDirty"
            class="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500"
            aria-label="Working tree has changes"
          />
        </Badge>
        <span
          v-if="isStreaming"
          class="flex items-center gap-1.5 text-xs text-primary"
        >
          <Loader2 class="h-3 w-3 animate-spin" />
          Streaming...
        </span>
        <span
          v-else-if="connected"
          class="text-xs text-emerald-500"
        >
          Connected
        </span>
      </div>

      <div class="flex items-center gap-2">
        <!-- Context warning badge -->
        <Badge
          v-if="contextWarning"
          variant="destructive"
          class="gap-1 text-xs"
        >
          <AlertTriangle class="h-3 w-3" />
          Context {{ contextPct }}%
        </Badge>
        <Button
          variant="ghost"
          size="icon-xs"
          :class="canvasStore.panelOpen ? 'text-primary' : 'text-muted-foreground'"
          title="Toggle execution canvas"
          aria-label="Toggle execution canvas"
          @click="canvasStore.panelOpen = !canvasStore.panelOpen"
        >
          <Workflow class="h-4 w-4" />
        </Button>
      </div>
    </div>

    <!-- Body: chat column + optional execution canvas.
         Below md the canvas is a full-size overlay (a 380px+ split column
         would crush the chat to zero width); above md it is a split panel. -->
    <div class="relative flex min-h-0 flex-1">
      <div class="flex min-w-0 flex-1 flex-col">
        <div class="relative min-h-0 flex-1">
          <!-- Messages area -->
          <div
            ref="scrollContainer"
            class="h-full overflow-y-auto"
            @scroll="handleScroll"
          >
            <!-- Empty state -->
            <div
              v-if="messages.length === 0 && !isLoadingHistory"
              class="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground"
            >
              <div class="flex h-16 w-16 items-center justify-center rounded-2xl bg-muted/50">
                <svg
                  class="h-8 w-8 text-muted-foreground/50"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  stroke-width="1.5"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    d="M8.625 12a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm0 0H8.25m4.125 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm0 0H12m4.125 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Zm0 0h-.375M21 12c0 4.556-4.03 8.25-9 8.25a9.764 9.764 0 0 1-2.555-.337A5.972 5.972 0 0 1 5.41 20.97a5.969 5.969 0 0 1-.474-.065 4.48 4.48 0 0 0 .978-2.025c.09-.457-.133-.901-.467-1.226C3.93 16.178 3 14.189 3 12c0-4.556 4.03-8.25 9-8.25s9 3.694 9 8.25Z"
                  />
                </svg>
              </div>
              <p class="text-sm">Start a conversation</p>
              <p class="text-xs text-muted-foreground/60">
                Type a message below to begin
              </p>
            </div>

            <!-- Loading indicator -->
            <div
              v-if="isLoadingHistory"
              class="flex h-full items-center justify-center"
            >
              <Loader2 class="h-6 w-6 animate-spin text-muted-foreground" />
            </div>

            <!-- Message list. Wrapper stays out of the empty state: its
                 padding would add dead scroll below the h-full empty panel
                 and pin a scrollbar over the composer. -->
            <div v-if="messages.length" class="py-4">
              <MessageBubble
                v-for="msg in messages"
                :key="msg.id"
                :message="msg"
                :streaming="isStreaming && msg === messages[messages.length - 1] && msg.role === 'agent'"
                @restore="openRestoreDialog(msg)"
              />
            </div>

            <!-- Sidekick advisory notes: quiet inline chips, never notifications -->
            <div v-if="sidekickNotes.length" class="px-4 pb-3">
              <div
                v-for="note in sidekickNotes"
                :key="note.id"
                class="mb-1 flex items-start gap-2 rounded-md border px-3 py-2 text-xs"
                :class="sidekickSeverityClass(note.severity)"
              >
                <component :is="sidekickIcon(note.severity)" class="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span class="leading-relaxed">{{ note.text }}</span>
              </div>
            </div>
          </div>
          <MessageRail
            :ticks="railTicks"
            :active-id="activeTickId"
            @select="jumpToMessage"
          />
        </div>

        <!-- Input area -->
        <ChatInput
          :disabled="false"
          @send="handleSend"
        />
      </div>

      <!-- Execution canvas panel: overlay below md, split column above -->
      <div
        v-if="canvasStore.panelOpen"
        class="absolute inset-0 z-20 bg-background md:relative md:inset-auto md:z-auto md:w-1/2 md:min-w-[380px] md:max-w-[60%] md:shrink-0 md:border-l md:border-border"
      >
        <ExecutionCanvas />
      </div>
    </div>

    <!-- Restore-to-here dialog (issue #21): pick the snapshot to rewind to;
         the snapshot before the clicked prompt is preselected. -->
    <Dialog :open="restoreDialogOpen" @update:open="restoreDialogOpen = $event">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Restore conversation</DialogTitle>
          <DialogDescription>
            The conversation returns to the state captured by the selected snapshot
            (everything after it is removed; the current state is kept as an undo
            snapshot). Pick one:
          </DialogDescription>
        </DialogHeader>
        <div class="max-h-72 space-y-1 overflow-y-auto" data-test="snapshot-list">
          <p v-if="!snapshots.length" class="px-2 py-4 text-sm text-muted-foreground">
            No snapshots yet — one is taken before every message you send.
          </p>
          <button
            v-for="snap in snapshots"
            :key="snap.name"
            type="button"
            class="flex w-full items-start gap-2 rounded-md border px-3 py-2 text-left text-sm transition-colors"
            :class="selectedSnapshot === snap.name ? 'border-primary bg-primary/5' : 'hover:bg-muted'"
            @click="selectedSnapshot = snap.name"
          >
            <span class="mt-0.5 h-3 w-3 shrink-0 rounded-full border" :class="selectedSnapshot === snap.name ? 'border-primary bg-primary' : 'border-muted-foreground/40'" />
            <span class="min-w-0 flex-1">
              <span class="block text-xs text-muted-foreground">
                {{ new Date(snap.created_at).toLocaleString() }} · {{ snap.messages }} message{{ snap.messages === 1 ? '' : 's' }}
                <Badge v-if="snap.trigger === 'pre-restore'" variant="outline" class="ml-1 px-1 py-0 text-[10px]">undo point</Badge>
              </span>
              <span v-if="snap.preview" class="mt-0.5 block truncate text-xs text-muted-foreground/80">{{ snap.preview }}</span>
            </span>
          </button>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="restoreDialogOpen = false">Cancel</Button>
          <Button :disabled="!selectedSnapshot || restoreBusy" @click="confirmRestore">
            {{ restoreBusy ? 'Restoring…' : 'Restore' }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
