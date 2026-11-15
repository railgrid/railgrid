export function servicePortError(value: unknown): string | null {
  const port = Number(value)
  return Number.isInteger(port) && port >= 1 && port <= 65535
    ? null
    : 'Enter a whole-number port from 1 to 65535.'
}

// Service proxies use a host, scheme and port rather than a URL path.
export function parseServiceHost(value: string | undefined, fixedScheme?: string): { host: string; scheme?: string; port?: number; error?: string } {
  const raw = value?.trim() || ''
  if (!/^https?:\/\//i.test(raw)) return { host: raw }
  try {
    const url = new URL(raw)
    const scheme = url.protocol.slice(0, -1)
    if (url.username || url.password) {
      return { host: raw, error: 'Remove credentials from the URL and configure them separately.' }
    }
    if (url.pathname !== '/' || url.search || url.hash) {
      return { host: raw, error: 'Use a root URL without a path, query, or fragment.' }
    }
    if (fixedScheme && scheme !== fixedScheme) {
      return { host: raw, error: `This service requires ${fixedScheme.toUpperCase()}. Use an ${fixedScheme}:// URL or enter a host name.` }
    }
    return { host: url.hostname, scheme, port: Number(url.port) || (scheme === 'https' ? 443 : 80) }
  } catch {
    return { host: raw, error: 'Enter a valid URL or host name.' }
  }
}

// Bracket IPv6 literals when appending a port; preserve already bracketed hosts.
export function displayServiceHost(host: string): string {
  return host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
}
