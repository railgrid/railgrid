import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'faros-theme'

function getSystemTheme(): 'light' | 'dark' {
  // Dark is the hard fallback (matches the CSS base) when matchMedia is
  // unavailable or throws.
  try {
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
      ? 'dark'
      : 'light'
  } catch {
    return 'dark'
  }
}

function applyTheme(resolved: 'light' | 'dark') {
  document.documentElement.classList.toggle('dark', resolved === 'dark')
  document.documentElement.classList.toggle('light', resolved === 'light')
}

export const useThemeStore = defineStore('theme', () => {
  // Dark is the product default; 'system' is opt-in via the toggle cycle.
  const mode = ref<ThemeMode>((localStorage.getItem(STORAGE_KEY) as ThemeMode) || 'dark')
  const resolved = ref<'light' | 'dark'>(
    mode.value === 'system' ? getSystemTheme() : mode.value,
  )

  function setMode(m: ThemeMode) {
    mode.value = m
    localStorage.setItem(STORAGE_KEY, m)
    resolved.value = m === 'system' ? getSystemTheme() : m
    applyTheme(resolved.value)
  }

  function toggle() {
    if (mode.value === 'dark') setMode('light')
    else if (mode.value === 'light') setMode('system')
    else setMode('dark')
  }

  // Listen for system theme changes
  const mql = window.matchMedia?.('(prefers-color-scheme: dark)')
  mql?.addEventListener('change', () => {
    if (mode.value === 'system') {
      resolved.value = getSystemTheme()
      applyTheme(resolved.value)
    }
  })

  // Apply on init
  applyTheme(resolved.value)

  return { mode, resolved, setMode, toggle }
})

/** Call before Vue mounts to prevent flash of wrong theme. */
export function initTheme() {
  const stored = localStorage.getItem(STORAGE_KEY) as ThemeMode | null
  const mode = stored || 'dark'
  const resolved = mode === 'system' ? getSystemTheme() : mode
  applyTheme(resolved)
}
