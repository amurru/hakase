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

export const useAppStore = defineStore('app', () => {
  const modelName = ref('gemini-2.5-flash')
  const connectionStatus = ref<'connected' | 'disconnected' | 'connecting'>('disconnected')
  const contextUsage = ref(0)
  const contextMax = ref(128000)
  const activeSessionTitle = ref('')
  const totalTokens = ref(0)
  const thinkingEnabled = ref(false)

  function setModelName(name: string) {
    modelName.value = name
  }

  // loadModelName pulls the real configured model from the backend so the UI
  // label reflects the user's configuration instead of the hardcoded default.
  async function loadModelName() {
    try {
      const resp = await apiGet<{ config: ConfigModelInfo }>('/config')
      setModelName(effectiveModelName(resp.config))
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

  function setTotalTokens(tokens: number) {
    totalTokens.value = tokens
  }

  function toggleThinking() {
    thinkingEnabled.value = !thinkingEnabled.value
  }

  return {
    modelName,
    connectionStatus,
    contextUsage,
    contextMax,
    activeSessionTitle,
    totalTokens,
    thinkingEnabled,
    setModelName,
    loadModelName,
    setConnectionStatus,
    setContextUsage,
    setActiveSessionTitle,
    setTotalTokens,
    toggleThinking,
  }
})
