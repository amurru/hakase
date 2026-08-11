import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { apiFetch } from '@/lib/api'

export interface ApprovalRequest {
  id: string
  tool: string
  risk: string
  reason: string
  command: string
}

export type ApprovalStatus = 'pending' | 'approved' | 'denied' | 'timed_out'

const COUNTDOWN_SECONDS = 60

export const useApprovalStore = defineStore('approval', () => {
  const currentApproval = ref<ApprovalRequest | null>(null)
  const queue = ref<ApprovalRequest[]>([])
  const status = ref<ApprovalStatus>('pending')
  const countdown = ref(COUNTDOWN_SECONDS)
  const responding = ref(false)

  let countdownTimer: ReturnType<typeof setInterval> | null = null
  let autoCloseTimer: ReturnType<typeof setTimeout> | null = null

  const isOpen = computed(() => currentApproval.value !== null)
  const progressPercent = computed(() =>
    Math.round((countdown.value / COUNTDOWN_SECONDS) * 100),
  )

  function clearTimers() {
    if (countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
    if (autoCloseTimer) {
      clearTimeout(autoCloseTimer)
      autoCloseTimer = null
    }
  }

  function startCountdown() {
    clearTimers()
    countdown.value = COUNTDOWN_SECONDS
    countdownTimer = setInterval(() => {
      if (countdown.value > 0) {
        countdown.value--
      }
      if (countdown.value <= 0) {
        handleTimeout()
      }
    }, 1000)
  }

  function handleTimeout() {
    clearTimers()
    status.value = 'timed_out'
    autoCloseTimer = setTimeout(() => {
      dismissCurrent()
    }, 2000)
  }

  function dismissCurrent() {
    clearTimers()
    currentApproval.value = null
    status.value = 'pending'

    if (queue.value.length > 0) {
      const next = queue.value.shift()!
      currentApproval.value = next
      status.value = 'pending'
      startCountdown()
    }
  }

  function handleApprovalEvent(data: ApprovalRequest) {
    if (currentApproval.value !== null) {
      queue.value.push(data)
      return
    }

    currentApproval.value = data
    status.value = 'pending'
    startCountdown()
  }

  function handleApprovalTimeout(id: string) {
    if (currentApproval.value?.id === id) {
      handleTimeout()
    } else {
      queue.value = queue.value.filter((a) => a.id !== id)
    }
  }

  async function respond(approved: boolean) {
    if (!currentApproval.value || responding.value) return

    responding.value = true
    clearTimers()

    try {
      await apiFetch(`/approvals/${currentApproval.value.id}/respond`, {
        method: 'POST',
        body: { approved },
      })
      status.value = approved ? 'approved' : 'denied'
    } catch {
      status.value = 'denied'
    } finally {
      responding.value = false
      dismissCurrent()
    }
  }

  function approve() {
    respond(true)
  }

  function deny() {
    respond(false)
  }

  return {
    currentApproval,
    status,
    countdown,
    responding,
    isOpen,
    progressPercent,
    handleApprovalEvent,
    handleApprovalTimeout,
    approve,
    deny,
  }
})
