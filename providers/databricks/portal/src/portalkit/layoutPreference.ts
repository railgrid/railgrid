export const layoutModes = ['grid', 'list'] as const

export type LayoutMode = typeof layoutModes[number]

export const DEFAULT_LAYOUT_MODE: LayoutMode = 'grid'

export interface LayoutPreferenceStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

export function isLayoutMode(value: unknown): value is LayoutMode {
  return value === 'grid' || value === 'list'
}

export function readLayoutPreference(
  key: string,
  storage?: LayoutPreferenceStorage | null,
): LayoutMode {
  try {
    const target = resolveStorage(storage)
    if (!target) return DEFAULT_LAYOUT_MODE
    const value = target.getItem(key)
    return isLayoutMode(value) ? value : DEFAULT_LAYOUT_MODE
  } catch {
    return DEFAULT_LAYOUT_MODE
  }
}

export function writeLayoutPreference(
  key: string,
  mode: LayoutMode,
  storage?: LayoutPreferenceStorage | null,
): void {
  try {
    resolveStorage(storage)?.setItem(key, mode)
  } catch {
    // Layout is a progressive preference. Storage policy or quota failures
    // must not interrupt the controlled UI state owned by the caller.
  }
}

export function nextLayoutMenuIndex(key: string, currentIndex: number): number | null {
  if (key === 'Home') return 0
  if (key === 'End') return layoutModes.length - 1
  if (key === 'ArrowDown') return currentIndex < layoutModes.length - 1 ? currentIndex + 1 : 0
  if (key === 'ArrowUp') return currentIndex > 0 ? currentIndex - 1 : layoutModes.length - 1
  return null
}

function resolveStorage(storage?: LayoutPreferenceStorage | null): LayoutPreferenceStorage | null {
  if (storage !== undefined) return storage
  if (typeof window === 'undefined') return null
  return window.localStorage
}
