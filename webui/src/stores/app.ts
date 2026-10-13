import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiGet } from '@/lib/api'

// Minimal slice of the /api/config response we need for the model label.
interface ConfigModelInfo {
  provider?: string
  model_name?: string
}

// effectiveModelName resolves the model the agent will actually use: the
// explicit model_name when set, otherwise the chosen provider's default.
function effectiveModelName(cfg: ConfigModelInfo): string {
  if (cfg.model_name && cfg.model_name.trim()) {
    return cfg.model_name.trim()
  }
  const provider = cfg.provider || 'gemini'
  if (provider === 'openai' || provider === 'openai-compatible') {
    return 'gpt-4o-mini'
  }
  return 'gemini-2.5-flash'
}

// Thinking-effort menu options for the composer selector. '' is the default:
// no per-message override, the server's configured thinking_level applies.
// The values mirror the backend allowlist (NormalizeThinkingLevel); 'maximum'
// and 'xhigh' are legacy aliases the server maps to 'high', so the menu only
// offers the canonical set.
export interface ThinkingLevelOption {
  value: string
  label: string
  hint: string
}

export const THINKING_LEVELS: ThinkingLevelOption[] = [
  { value: '', label: 'Default', hint: 'Server setting' },
  { value: 'off', label: 'Off', hint: 'No reasoning' },
  { value: 'minimal', label: 'Minimal', hint: 'Fastest' },
  { value: 'low', label: 'Low', hint: 'Quick' },
  { value: 'medium', label: 'Medium', hint: 'Balanced' },
  { value: 'high', label: 'High', hint: 'Deepest' },
]

const THINKING_LEVEL_STORAGE_KEY = 'hakase-thinking-level'

function loadStoredThinkingLevel(): string {
  try {
    const raw = localStorage.getItem(THINKING_LEVEL_STORAGE_KEY) ?? ''
    return THINKING_LEVELS.some((o) => o.value === raw) ? raw : ''
  } catch {
    return ''
  }
}

export const useAppStore = defineStore('app', () => {
  const modelName = ref('gemini-2.5-flash')
  const connectionStatus = ref<'connected' | 'disconnected' | 'connecting'>('disconnected')
  const contextUsage = ref(0)
  const contextMax = ref(128000)
  const activeSessionTitle = ref('')
  // activeProjectName is the registered-project binding of the active session
  // (empty for plain local sessions); the chat header renders it as a chip.
  const activeProjectName = ref('')
  const totalTokens = ref(0)
  const cachedTokens = ref(0)
  const costUsd = ref(0)
  const budgetPct = ref(0)
  // Per-message thinking-effort override, sticky across reloads. '' means no
  // override (the server's configured thinking_level applies); any other
  // value is sent as thinking_level with each chat POST.
  const thinkingLevel = ref(loadStoredThinkingLevel())

  function setModelName(name: string) {
    modelName.value = name
  }

  // loadModelName pulls the real configured model from the backend so the UI
  // label reflects the user's configuration instead of the hardcoded default.
  // The backend resolves the effective model (explicit model_name or the
  // provider default) and returns it as effective_model; the local helper is a
  // fallback for older servers that don't populate it.
  async function loadModelName() {
    try {
      const resp = await apiGet<{ config: ConfigModelInfo; effective_model?: string }>('/config')
      const effective = resp.effective_model?.trim()
      setModelName(effective ? effective : effectiveModelName(resp.config))
    } catch {
      // Keep the current value if the request fails (e.g. auth race); the next
      // call after a successful request will correct it.
    }
  }

  function setConnectionStatus(status: 'connected' | 'disconnected' | 'connecting') {
    connectionStatus.value = status
  }

  function setContextUsage(used: number, max?: number) {
    contextUsage.value = used
    if (max !== undefined) {
      contextMax.value = max
    }
  }

  function setActiveSessionTitle(title: string) {
    activeSessionTitle.value = title
  }

  function setActiveProjectName(name: string) {
    activeProjectName.value = name
  }

  function setTotalTokens(tokens: number) {
    totalTokens.value = tokens
  }

  // setCachedTokens records the prompt-cache read from SSE usage events
  // (PC-007 additive field; older servers omit it and the chip stays 0).
  function setCachedTokens(tokens: number) {
    cachedTokens.value = tokens
  }

  // setCostMeter records the turn cost and budget fill from SSE v2 usage
  // events (additive fields; older servers omit them and the meter stays 0).
  function setCostMeter(cost: number, pct: number) {
    if (typeof cost === 'number' && cost >= 0) costUsd.value = cost
    if (typeof pct === 'number' && pct >= 0) budgetPct.value = Math.min(100, Math.round(pct))
  }

  // setThinkingLevel records the composer's effort selection and persists
  // it. Unknown values reset to the server default rather than sending a
  // level the backend would reject.
  function setThinkingLevel(level: string) {
    const next = THINKING_LEVELS.some((o) => o.value === level) ? level : ''
    thinkingLevel.value = next
    try {
      localStorage.setItem(THINKING_LEVEL_STORAGE_KEY, next)
    } catch {
      // Storage unavailable (private mode) - the selection still holds for
      // this session.
    }
  }

  function thinkingLevelLabel(level: string): string {
    return THINKING_LEVELS.find((o) => o.value === level)?.label ?? 'Default'
  }

  return {
    modelName,
    connectionStatus,
    contextUsage,
    contextMax,
    activeSessionTitle,
    activeProjectName,
    totalTokens,
    cachedTokens,
    costUsd,
    budgetPct,
    thinkingLevel,
    setCostMeter,
    setThinkingLevel,
    thinkingLevelLabel,
    setModelName,
    loadModelName,
    setConnectionStatus,
    setContextUsage,
    setActiveSessionTitle,
    setActiveProjectName,
    setTotalTokens,
    setCachedTokens,
  }
})
