import { ref } from 'vue'

const SIDEBAR_EXPANDED_KEY = 'faros-sidebar-expanded'

function readSidebarExpansion(): boolean {
  try {
    if (typeof window === 'undefined') return false
    return window.localStorage.getItem(SIDEBAR_EXPANDED_KEY) === '1'
  } catch {
    // Storage is optional: SSR, private browsing, and blocked storage should
    // not prevent the shell from mounting.
    return false
  }
}

function writeSidebarExpansion(expanded: boolean): void {
  try {
    if (typeof window === 'undefined') return
    window.localStorage.setItem(SIDEBAR_EXPANDED_KEY, expanded ? '1' : '0')
  } catch {
    // A preference miss is non-fatal. Keep the in-memory state authoritative.
  }
}

/**
 * Shared expansion state for host shell rails. AppLayout and the platform-admin
 * shell are mutually exclusive routes, while localStorage keeps their geometry
 * consistent across navigation and reloads.
 */
export function useSidebarExpansion() {
  const sidebarExpanded = ref(readSidebarExpansion())

  function toggleSidebar() {
    sidebarExpanded.value = !sidebarExpanded.value
    writeSidebarExpansion(sidebarExpanded.value)
  }

  return { sidebarExpanded, toggleSidebar }
}
