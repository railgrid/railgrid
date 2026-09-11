const maskedJoinToken = '••••••••••••••••'

export const MACOS_MASKED_JOIN_TOKEN = maskedJoinToken

function currentOrigin(): string {
  return typeof window === 'undefined' ? '' : window.location.origin
}

/** Build the hub URL that a host agent should use for the active workspace. */
export function hubURLForCluster(cluster: string | null, origin = currentOrigin()): string {
  const base = origin.replace(/\/+$/, '')
  return cluster ? `${base}/clusters/${cluster}` : base
}

/**
 * Render the persistent macOS LaunchDaemon install command.
 *
 * The one-time token is the only secret in this command. Callers can pass the
 * masked value for display and the real token for an explicit clipboard copy.
 */
export function macosJoinSnippet(
  edgeName: string,
  cluster: string | null,
  token: string,
  origin?: string,
): string {
  const lines = [
    'sudo faros agent join',
    '  --hub-url ' + hubURLForCluster(cluster, origin),
    '  --edge-name ' + edgeName,
    '  --type macos',
    '  --worker-user "$(id -un)"',
  ]
  if (cluster && cluster !== 'default') lines.push('  --cluster ' + cluster)
  lines.push('  --token ' + token)
  return lines.join(' ' + String.fromCharCode(92) + '\n')
}
