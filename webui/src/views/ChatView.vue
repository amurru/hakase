<script setup lang="ts">
import { ref, watch, nextTick, onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { useApprovalStore } from '@/stores/approval'
import { useClarifyStore } from '@/stores/clarify'
import { useSSE } from '@/composables/useSSE'
import { useNotifications } from '@/composables/useNotifications'
import { apiFetch } from '@/lib/api'
import MessageBubble from '@/components/chat/MessageBubble.vue'
import ChatInput from '@/components/chat/ChatInput.vue'
import type { FileAttachment } from '@/components/chat/AttachmentPicker.vue'
import { AlertTriangle, Loader2 } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'

const route = useRoute()
const appStore = useAppStore()
const approvalStore = useApprovalStore()
const clarifyStore = useClarifyStore()

const sessionId = ref<string | null>(null)
const isLoadingHistory = ref(false)
const scrollContainer = ref<HTMLDivElement | null>(null)
const isUserScrolledUp = ref(false)

// Initialize SSE composable
const {
  messages,
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
}

// Scroll to bottom when new messages arrive (if not scrolled up)
watch(
  () => messages.value.length,
  () => {
    if (!isUserScrolledUp.value) {
      scrollToBottom()
    }
  },
)

// Scroll to bottom when streaming content grows
watch(
  () => messages.value[messages.value.length - 1]?.content,
  () => {
    if (!isUserScrolledUp.value) {
      scrollToBottom()
    }
  },
)

// Load session history
async function loadSessionHistory(sid: string) {
  isLoadingHistory.value = true
  try {
    const data = await apiFetch<{ messages?: Array<{ role: string; content: string; thinking?: string }> }>(
      `/sessions/${sid}`,
    )
    if (data.messages) {
      for (const msg of data.messages) {
        messages.value.push({
          id: `hist-${Date.now()}-${Math.random()}`,
          role: msg.role === 'user' ? 'user' : 'agent',
          content: msg.content,
          thinking: msg.thinking ?? '',
          timestamp: Date.now(),
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
  }
})
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header with session info + context warning -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-4 py-2">
      <div class="flex items-center gap-3">
        <span class="text-sm font-medium text-foreground">
          {{ appStore.activeSessionTitle || 'New Session' }}
        </span>
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

      <!-- Context warning badge -->
      <Badge
        v-if="contextWarning"
        variant="destructive"
        class="gap-1 text-xs"
      >
        <AlertTriangle class="h-3 w-3" />
        Context {{ contextPct }}%
      </Badge>
    </div>

    <!-- Messages area -->
    <div
      ref="scrollContainer"
      class="flex-1 overflow-y-auto"
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

      <!-- Message list -->
      <div class="py-4">
        <MessageBubble
          v-for="msg in messages"
          :key="msg.id"
          :message="msg"
          :streaming="isStreaming && msg === messages[messages.length - 1] && msg.role === 'agent'"
        />
      </div>
    </div>

    <!-- Input area -->
    <ChatInput
      :disabled="false"
      @send="handleSend"
    />
  </div>
</template>
