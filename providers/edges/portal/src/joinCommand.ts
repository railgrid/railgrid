import type { EdgeType } from './types'

// The CLI reads the selected workspace from its current kubeconfig.
export function joinCommandFallback(type: EdgeType, name: string): string {
  const reference = `${type}/${name}`.replace(/'/g, "'\\''")
  return `In a terminal logged in to the same hub and workspace, run: railgrid edge join-command '${reference}'`
}
