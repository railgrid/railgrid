import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'faros-theme'

function getSystemTheme(): 'light' | 'dark' {
  // Dark is the hard fallback (matches the CSS base) when matchMedia is
  // unavailable or throws.
  try {
    if (typeof window.matchMedia !== 'function') return 'dark'
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  } catch {
    return 'dark'
  }
}

function readStoredMode(): ThemeMode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    return stored === 'light' || stored === 'dark' || stored === 'system' ? stored : 'dark'
  } catch {
    return 'dark'
  }
}

function storeMode(mode: ThemeMode): void {
  try {
    localStorage.setItem(STORAGE_KEY, mode)
  } catch {
    // A storage-disabled browser still gets the selected theme for this load.
  }
}

function applyTheme(resolved: 'light' | 'dark') {
  document.documentElement.classList.toggle('dark', resolved === 'dark')
  document.documentElement.classList.toggle('light', resolved === 'light')
  document.querySelector<HTMLMetaElement>('#faros-color-scheme')?.setAttribute('content', resolved)
  document.documentElement.style.colorScheme = resolved
}

export const useThemeStore = defineStore('theme', () => {
  // No stored preference is deliberately dark. Following the OS remains an
  // explicit choice in the account menu (`system`), rather than an implicit
  // first-paint dependency.
  const mode = ref<ThemeMode>(readStoredMode())
  const resolved = ref<'light' | 'dark'>(
    mode.value === 'system' ? getSystemTheme() : mode.value,
  )

  function setMode(m: ThemeMode) {
    mode.value = m
    storeMode(m)
    resolved.value = m === 'system' ? getSystemTheme() : m
    applyTheme(resolved.value)
  }

  function toggle() {
    if (mode.value === 'dark') setMode('light')
    else if (mode.value === 'light') setMode('system')
    else setMode('dark')
  }

  // Listen for system theme changes
  let mql: MediaQueryList | undefined
  try {
    mql = typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-color-scheme: dark)')
      : undefined
  } catch {
    mql = undefined
  }
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
  const mode = readStoredMode()
  const resolved = mode === 'system' ? getSystemTheme() : mode
  applyTheme(resolved)
}
