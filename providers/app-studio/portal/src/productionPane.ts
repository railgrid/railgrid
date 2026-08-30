export function shortReleaseSHA(value: string | null | undefined, length = 8): string {
  const normalized = typeof value === 'string' ? value.trim() : ''
  return normalized.length > length ? normalized.slice(0, length) : normalized
}

function numericValue(values: Record<string, unknown>, keys: string[]): number | null {
  for (const key of keys) {
    const value = values[key]
    if (typeof value === 'number' && Number.isFinite(value)) return value
    if (typeof value === 'string' && value.trim() && Number.isFinite(Number(value))) return Number(value)
  }
  return null
}

function objectValue(values: Record<string, unknown>, key: string): Record<string, unknown> | null {
  const value = values[key]
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

function environmentOverrideCount(values: Record<string, unknown>): number {
  return ['env', 'webEnv', 'apiEnv', 'workerEnv'].reduce((count, key) => {
    const entries = objectValue(values, key)
    return count + (entries ? Object.keys(entries).length : 0)
  }, 0)
}

export function productionConfigurationSummary(values: Record<string, unknown> | null | undefined): string {
  const source = values ?? {}
  const parts: string[] = []
  const replicas = numericValue(source, ['replicas', 'replicaCount'])
  const port = numericValue(source, ['port', 'webPort', 'frontendPort'])
  const expose = objectValue(source, 'expose')
  const hostnamePrefix = typeof expose?.hostnamePrefix === 'string' ? expose.hostnamePrefix.trim() : ''
  const overrideCount = environmentOverrideCount(source)

  if (replicas !== null) parts.push(`${replicas} replica${replicas === 1 ? '' : 's'}`)
  if (port !== null) parts.push(`port ${port}`)
  parts.push(hostnamePrefix ? 'custom hostname' : 'default hostname')
  parts.push(overrideCount ? `${overrideCount} environment override${overrideCount === 1 ? '' : 's'}` : 'no environment overrides')
  return parts.join(' · ')
}
