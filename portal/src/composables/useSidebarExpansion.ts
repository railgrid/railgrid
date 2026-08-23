import { ref } from 'vue'

const SIDEBAR_EXPANDED_KEY = 'faros-sidebar-expanded'

/**
 * Shared expansion state for host shell rails. AppLayout and the platform-admin
 * shell are mutually exclusive routes, while localStorage keeps their geometry
 * consistent across navigation and reloads.
 */
export function useSidebarExpansion() {
  const sidebarExpanded = ref(localStorage.getItem(SIDEBAR_EXPANDED_KEY) === '1')

  function toggleSidebar() {
    sidebarExpanded.value = !sidebarExpanded.value
    localStorage.setItem(SIDEBAR_EXPANDED_KEY, sidebarExpanded.value ? '1' : '0')
  }

  return { sidebarExpanded, toggleSidebar }
}
