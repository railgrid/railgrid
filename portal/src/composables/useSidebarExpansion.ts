import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const SIDEBAR_EXPANDED_KEY = 'railgrid-sidebar-expanded'

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
  const desktopExpanded = ref(readSidebarExpansion())
  const media = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(max-width: 640px)') : null
  const narrow = ref(media?.matches ?? false)
  const narrowExpanded = ref(false)
  const sidebarExpanded = computed(() => narrow.value ? narrowExpanded.value : desktopExpanded.value)

  function onViewportChange(event: MediaQueryListEvent) {
    narrow.value = event.matches
    narrowExpanded.value = false
  }
  onMounted(() => media?.addEventListener('change', onViewportChange))
  onBeforeUnmount(() => media?.removeEventListener('change', onViewportChange))

  function toggleSidebar() {
    if (narrow.value) narrowExpanded.value = !narrowExpanded.value
    else {
      desktopExpanded.value = !desktopExpanded.value
      writeSidebarExpansion(desktopExpanded.value)
    }
  }
  function closeNarrowSidebar() { narrowExpanded.value = false }

  return { sidebarExpanded, narrow, toggleSidebar, closeNarrowSidebar }
}
