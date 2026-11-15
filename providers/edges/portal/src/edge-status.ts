/** Include connectivity in the visible label when a disconnected edge reports a phase. */
export function edgeStatusLabel(edge: { connected: boolean; phase?: string | null } | null, loading: boolean, deleting: boolean): string {
  if (deleting) return 'Deleting'
  if (!edge) return loading ? 'Loading' : 'Unavailable'
  if (edge.connected) return 'Connected'
  const phase = edge.phase?.trim()
  if (!phase || phase.toLowerCase() === 'disconnected') return 'Disconnected'
  return `${phase} · disconnected`
}
