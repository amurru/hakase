import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { apiFetch } from '@/lib/api'

export interface ClarifyItem {
  id: string
  question: string
  choices: string[]
  multiSelect: boolean
  arrivedAt: number
}

export type ClarifyStatus = 'pending' | 'answered' | 'canceled' | 'timed_out'

const TIMEOUT_SECONDS = 120

export const useClarifyStore = defineStore('clarify', () => {
  const queue = ref<ClarifyItem[]>([])
  const status = ref<ClarifyStatus>('pending')
  const countdown = ref(TIMEOUT_SECONDS)
  let timerHandle: ReturnType<typeof setInterval> | null = null

  const currentClarify = computed(() => queue.value[0] ?? null)
  const isOpen = computed(() => status.value === 'pending' && currentClarify.value !== null)

  function handleClarifyEvent(data: {
    id: string
    question: string
    choices?: string[]
    multi_select?: boolean
  }) {
    const item: ClarifyItem = {
      id: data.id,
      question: data.question,
      choices: data.choices ?? [],
      multiSelect: data.multi_select ?? false,
      arrivedAt: Date.now(),
    }
    queue.value.push(item)

    // Start countdown if this is the first item.
    if (queue.value.length === 1) {
      startCountdown()
    }
  }

  function handleClarifyTimeout(data: { id: string }) {
    // If the timed-out item is the current one, stop and mark.
    if (currentClarify.value?.id === data.id) {
      stopTimer()
      status.value = 'timed_out'
      // Auto-close after a brief display.
      setTimeout(() => dismissCurrent(), 1500)
    } else {
      // Remove from queue if it was queued.
      queue.value = queue.value.filter((q) => q.id !== data.id)
    }
  }

  async function respond(
    choices: string[],
    answer?: string,
  ) {
    const item = currentClarify.value
    if (!item) return

    stopTimer()
    status.value = 'answered'

    const body: Record<string, unknown> = choices.length > 0
      ? { choices }
      : { answer: answer ?? '' }

    try {
      await apiFetch(`/clarifications/${item.id}/respond`, {
        method: 'POST',
        body,
      })
    } catch {
      // Silently handle - backend may have already timed out.
    }

    setTimeout(() => dismissCurrent(), 300)
  }

  async function cancel() {
    const item = currentClarify.value
    if (!item) return

    stopTimer()
    status.value = 'canceled'

    // Send empty choices to signal cancellation.
    try {
      await apiFetch(`/clarifications/${item.id}/respond`, {
        method: 'POST',
        body: { choices: [] },
      })
    } catch {
      // Silently handle.
    }

    setTimeout(() => dismissCurrent(), 300)
  }

  function dismissCurrent() {
    queue.value = queue.value.slice(1)
    status.value = 'pending'
    countdown.value = TIMEOUT_SECONDS

    // Start countdown for the next queued item, if any.
    if (queue.value.length > 0) {
      startCountdown()
    }
  }

  function startCountdown() {
    stopTimer()
    countdown.value = TIMEOUT_SECONDS
    status.value = 'pending'

    timerHandle = setInterval(() => {
      countdown.value--
      if (countdown.value <= 0) {
        stopTimer()
        status.value = 'timed_out'
        // Auto-respond with TimedOut.
        const item = currentClarify.value
        if (item) {
          apiFetch(`/clarifications/${item.id}/respond`, {
            method: 'POST',
            body: { answer: 'TimedOut' },
          }).catch(() => {})
        }
        setTimeout(() => dismissCurrent(), 1500)
      }
    }, 1000)
  }

  function stopTimer() {
    if (timerHandle !== null) {
      clearInterval(timerHandle)
      timerHandle = null
    }
  }

  return {
    queue,
    status,
    countdown,
    currentClarify,
    isOpen,
    handleClarifyEvent,
    handleClarifyTimeout,
    respond,
    cancel,
    dismissCurrent,
  }
})
