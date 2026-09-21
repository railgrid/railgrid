import { isWorkspaceAvailable, isWorkspaceUsable, type WorkspaceRow } from '@/stores/tenant'

// Never guess between multiple workspaces, including ones still provisioning.
export function preferredWorkspace(rows: WorkspaceRow[], remembered: string | null): WorkspaceRow | null {
  const available = rows.filter(isWorkspaceAvailable)
  const previous = available.find(row => row.uuid === remembered)
  if (previous) return isWorkspaceUsable(previous) ? previous : null
  return available.length === 1 && isWorkspaceUsable(available[0]) ? available[0]! : null
}
