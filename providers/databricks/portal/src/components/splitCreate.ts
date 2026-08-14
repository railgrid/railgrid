export type SplitCreateAction = 'browse' | 'manual'

export type SplitCreateKind = 'warehouse' | 'table'

export const splitCreateMenuActions: readonly SplitCreateAction[] = ['browse', 'manual']

export function splitCreateLabel(kind: SplitCreateKind): string {
  return `New ${kind}`
}

export function splitCreatePrimaryAction(): SplitCreateAction {
  return 'browse'
}

export function splitCreateMenuDismissesOnKey(key: string): boolean {
  return key === 'Escape' || key === 'Tab'
}

export function nextSplitCreateMenuIndex(key: string, currentIndex: number, count: number): number | null {
  if (count < 1) return null
  if (key === 'Home') return 0
  if (key === 'End') return count - 1
  if (key === 'ArrowDown') return currentIndex < count - 1 ? currentIndex + 1 : 0
  if (key === 'ArrowUp') return currentIndex > 0 ? currentIndex - 1 : count - 1
  return null
}
