import { defineStore } from 'pinia'
import { ref } from 'vue'
import { applyHighlightTheme } from '@/lib/highlightTheme'

export type Theme = 'dark' | 'light'

const STORAGE_KEY = 'hakase_theme'

function readStoredTheme(): Theme {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'light' || stored === 'dark') return stored
  return 'dark'
}

function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle('dark', theme === 'dark')
  applyHighlightTheme(theme)
}

export const useThemeStore = defineStore('theme', () => {
  const theme = ref<Theme>(readStoredTheme())

  // Apply the stored theme on store init so the visual theme matches the
  // persisted value even if the head bootstrap script (theme-init.js) is
  // blocked or unavailable. Idempotent with classList.toggle. Also injects the
  // matching highlight.js theme stylesheet.
  applyTheme(theme.value)

  function setTheme(next: Theme) {
    theme.value = next
    applyTheme(next)
    localStorage.setItem(STORAGE_KEY, next)
  }

  function toggleTheme() {
    setTheme(theme.value === 'dark' ? 'light' : 'dark')
  }

  return { theme, setTheme, toggleTheme }
})
