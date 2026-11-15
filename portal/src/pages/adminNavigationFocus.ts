// A scoped route transition may remount the admin shell. Retain only the
// explicit section-link intent long enough for its destination to claim focus.
let pendingPath = ''

export function rememberAdminNavigation(event: MouseEvent, path: string, currentPath: string): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || path === currentPath) return
  pendingPath = path
}

export function consumeAdminNavigation(path: string): boolean {
  const matches = pendingPath === path
  pendingPath = ''
  return matches
}
