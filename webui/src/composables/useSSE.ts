import { ref, onUnmounted, readonly } from 'vue'
import { apiFetch } from '@/lib/api'
import { useAppStore } from '@/stores/app'

export interface ChatMessage {
  id: string
  role: 'user' | 'agent'
  content: string
  thinking: string
  timestamp: number
}

export interface SSEApproval {
  id: string
  tool: string
  risk: string
  reason: string
  command: string
}

export interface SSEClarify {
  id: string
  question: string
  choices: string[]
  multi_select: boolean
}

export interface SSEUsage {
  tokens: number
  percent: number
}

export function useSSE(sessionId: () => string | null) {
  const messages = ref<ChatMessage[]>([])
  const isStreaming = ref(false)
  const connected = ref(false)
  const lastError = ref<string | null>(null)

  // Event callbacks (set by consumer)
  let onApproval: ((data: SSEApproval) => void) | null = null
  let onApprovalTimeout: ((id: string) => void) | null = null
  let onClarify: ((data: SSEClarify) => void) | null = null
  let onClarifyTimeout: ((data: { id: string }) => void) | null = null
  let onLog: ((line: string) => void) | null = null
  let onTask: ((data: Record<string, unknown>) => void) | null = null
  let onDelegation: ((data: Record<string, unknown>) => void) | null = null
  let onCron: ((data: Record<string, unknown>) => void) | null = null

  let eventSource: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectDelay = 1000
  let doneReceived = false

  const appStore = useAppStore()

  function appendToLastAgent(delta: { content?: string; thinking?: string }) {
    const lastMsg = messages.value[messages.value.length - 1]
    if (lastMsg && lastMsg.role === 'agent') {
      if (delta.content) lastMsg.content += delta.content
      if (delta.thinking) lastMsg.thinking += delta.thinking
    }
  }

  function ensureAgentMessage() {
    const lastMsg = messages.value[messages.value.length - 1]
    if (!lastMsg || lastMsg.role !== 'agent') {
      messages.value.push({
        id: `stream-${Date.now()}`,
        role: 'agent',
        content: '',
        thinking: '',
        timestamp: Date.now(),
      })
    }
  }

  function connect(sid: string) {
    if (eventSource) {
      eventSource.close()
    }

    doneReceived = false
    appStore.setConnectionStatus('connecting')

    // EventSource uses cookie-based auth (HttpOnly cookie sent automatically)
    eventSource = new EventSource(`/api/sessions/${sid}/stream`)

    eventSource.onopen = () => {
      connected.value = true
      lastError.value = null
      reconnectDelay = 1000
      appStore.setConnectionStatus('connected')
    }

    eventSource.onerror = () => {
      connected.value = false
      appStore.setConnectionStatus('disconnected')

      if (!doneReceived) {
        scheduleReconnect(sid)
      }
    }

    // stream events: progressive content/thinking deltas
    eventSource.addEventListener('stream', (e) => {
      try {
        const data = JSON.parse(e.data)
        ensureAgentMessage()
        appendToLastAgent({
          content: data.content ?? undefined,
          thinking: data.thinking ?? undefined,
        })
        isStreaming.value = true
      } catch {
        // ignore parse errors
      }
    })

    // message events: complete message (for final content)
    eventSource.addEventListener('message', (e) => {
      try {
        const data = JSON.parse(e.data)
        ensureAgentMessage()
        appendToLastAgent({
          content: data.content ?? undefined,
          thinking: data.thinking ?? undefined,
        })
      } catch {
        // ignore
      }
    })

    // log events: agent execution logs
    eventSource.addEventListener('log', (e) => {
      try {
        const data = JSON.parse(e.data)
        onLog?.(typeof data === 'string' ? data : data.line ?? JSON.stringify(data))
      } catch {
        // ignore
      }
    })

    // done event: agent run finished
    eventSource.addEventListener('done', () => {
      doneReceived = true
      isStreaming.value = false
      eventSource?.close()
      eventSource = null
      appStore.setConnectionStatus('connected')
    })

    // usage event: context usage update
    eventSource.addEventListener('usage', (e) => {
      try {
        const data: SSEUsage = JSON.parse(e.data)
        appStore.setContextUsage(data.tokens, undefined)
        // percent from the server overrides our calculation
        if (data.percent > 0) {
          appStore.setContextUsage(Math.round((data.percent / 100) * appStore.contextMax))
        }
      } catch {
        // ignore
      }
    })

    // approval event: agent requests approval
    eventSource.addEventListener('approval', (e) => {
      try {
        const data: SSEApproval = JSON.parse(e.data)
        onApproval?.(data)
      } catch {
        // ignore
      }
    })

    // clarify event: agent asks a question
    eventSource.addEventListener('clarify', (e) => {
      try {
        const data: SSEClarify = JSON.parse(e.data)
        onClarify?.(data)
      } catch {
        // ignore
      }
    })

    // approval_timeout event: approval timed out
    eventSource.addEventListener('approval_timeout', (e) => {
      try {
        const data = JSON.parse(e.data)
        const id = typeof data === 'string' ? data : data.id
        if (id) onApprovalTimeout?.(id)
      } catch {
        // ignore
      }
    })

    // clarify_timeout event: clarification timed out
    eventSource.addEventListener('clarify_timeout', (e) => {
      try {
        const data = JSON.parse(e.data)
        onClarifyTimeout?.(typeof data === 'object' ? data : { id: data })
      } catch {
        // ignore
      }
    })

    // task event: task board update
    eventSource.addEventListener('task', (e) => {
      try {
        const data = JSON.parse(e.data)
        onTask?.(data)
      } catch {
        // ignore
      }
    })

    // delegation event: sub-agent delegation status
    eventSource.addEventListener('delegation', (e) => {
      try {
        const data = JSON.parse(e.data)
        onDelegation?.(data)
      } catch {
        // ignore
      }
    })

    // cron event: cron job status
    eventSource.addEventListener('cron', (e) => {
      try {
        const data = JSON.parse(e.data)
        onCron?.(data)
      } catch {
        // ignore
      }
    })
  }

  function scheduleReconnect(sid: string) {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
    }
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      reconnectDelay = Math.min(reconnectDelay * 2, 30000)
      connect(sid)
    }, reconnectDelay)
  }

  async function sendMessage(content: string) {
    const sid = sessionId()
    if (!sid || !content.trim()) return

    // Add user message to the list
    messages.value.push({
      id: `user-${Date.now()}`,
      role: 'user',
      content: content.trim(),
      thinking: '',
      timestamp: Date.now(),
    })

    try {
      await apiFetch(`/sessions/${sid}/messages`, {
        method: 'POST',
        body: { content: content.trim() },
      })

      // Start SSE connection if not already connected
      if (!eventSource || eventSource.readyState === EventSource.CLOSED) {
        isStreaming.value = true
        connect(sid)
      }
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : 'Failed to send message'
      isStreaming.value = false
    }
  }

  function clearMessages() {
    messages.value = []
  }

  // Event registration helpers
  function onApprovalEvent(handler: (data: SSEApproval) => void) {
    onApproval = handler
  }

  function onApprovalTimeoutEvent(handler: (id: string) => void) {
    onApprovalTimeout = handler
  }

  function onClarifyEvent(handler: (data: SSEClarify) => void) {
    onClarify = handler
  }

  function onClarifyTimeoutEvent(handler: (data: { id: string }) => void) {
    onClarifyTimeout = handler
  }

  function onLogEvent(handler: (line: string) => void) {
    onLog = handler
  }

  function onTaskEvent(handler: (data: Record<string, unknown>) => void) {
    onTask = handler
  }

  function onDelegationEvent(handler: (data: Record<string, unknown>) => void) {
    onDelegation = handler
  }

  function onCronEvent(handler: (data: Record<string, unknown>) => void) {
    onCron = handler
  }

  onUnmounted(() => {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
    }
    if (eventSource) {
      doneReceived = true // prevent reconnect
      eventSource.close()
      eventSource = null
    }
  })

  return {
    messages,
    isStreaming: readonly(isStreaming),
    connected: readonly(connected),
    lastError: readonly(lastError),
    sendMessage,
    clearMessages,
    connect,
    onApprovalEvent,
    onApprovalTimeoutEvent,
    onClarifyEvent,
    onClarifyTimeoutEvent,
    onLogEvent,
    onTaskEvent,
    onDelegationEvent,
    onCronEvent,
  }
}
