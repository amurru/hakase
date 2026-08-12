import { defineStore } from 'pinia'
import { ref } from 'vue'

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
    setConnectionStatus,
    setContextUsage,
    setActiveSessionTitle,
    setTotalTokens,
    toggleThinking,
  }
})
