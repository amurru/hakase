import { watch } from 'vue'
import { toast } from 'vue-sonner'
import { useAppStore } from '@/stores/app'

/** Last threshold we warned at (so we only warn on upward crossings). */
let lastWarnedThreshold = 0

/**
 * Setup notification handlers for SSE events and context usage.
 * Returns handlers to wire into useSSE event setters.
 */
export function useNotifications() {
  const appStore = useAppStore()

  // --- Delegation handler ---
  function handleDelegation(data: Record<string, unknown>) {
    const status = (data.status as string) ?? 'unknown'
    const agent = (data.agent as string) ?? 'Sub-agent'
    const message = (data.message as string) ?? ''

    const detail = message ? `: ${message}` : ''

    switch (status) {
      case 'started':
        toast.info(`${agent} started${detail}`, { duration: 5000 })
        break
      case 'completed':
        toast.success(`${agent} completed${detail}`, { duration: 5000 })
        break
      case 'failed':
        toast.error(`${agent} failed${detail}`, { duration: 8000 })
        break
      default:
        toast(`${agent}: ${status}${detail}`, { duration: 5000 })
    }
  }

  // --- Cron handler ---
  function handleCron(data: Record<string, unknown>) {
    const status = (data.status as string) ?? 'unknown'
    const name = (data.name as string) ?? (data.jobID as string) ?? 'Cron job'
    const summary = (data.summary as string) ?? ''

    const detail = summary ? `: ${summary}` : ''

    switch (status) {
      case 'started':
        toast.info(`Cron "${name}" started${detail}`, { duration: 5000 })
        break
      case 'completed':
        toast.success(`Cron "${name}" finished${detail}`, { duration: 5000 })
        break
      case 'failed':
        toast.error(`Cron "${name}" failed${detail}`, { duration: 8000 })
        break
      default:
        toast(`Cron "${name}": ${status}${detail}`, { duration: 5000 })
    }
  }

  // --- Context usage watcher ---
  // Throttled: only warn on upward threshold crossings (80%, 90%, 100%).
  watch(
    () => appStore.contextUsage,
    (used) => {
      if (appStore.contextMax === 0) return
      const pct = Math.round((used / appStore.contextMax) * 100)
      const threshold = pct >= 100 ? 100 : pct >= 90 ? 90 : pct >= 80 ? 80 : 0

      if (threshold > lastWarnedThreshold) {
        lastWarnedThreshold = threshold
        toast.warning(`Context usage at ${pct}%`, { duration: 6000 })
      }

      // Reset threshold if usage drops back below the current level
      // (e.g. after a compact/compaction reduces usage).
      if (threshold < lastWarnedThreshold) {
        lastWarnedThreshold = threshold
      }
    },
  )

  return {
    handleDelegation,
    handleCron,
  }
}
