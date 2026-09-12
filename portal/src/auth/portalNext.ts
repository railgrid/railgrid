// Per-tab, single-use continuation for the portal's existing login flows.
const KEY = 'faros.portal-next'

export function validPortalNext(value: unknown): string | null {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\\x00-\x20]/.test(value)) return null
  try {
    const url = new URL(value, 'https://faros.invalid')
    if (url.origin !== 'https://faros.invalid') return null
    if (/^\/(?:login|auth)(?:\/|$)/.test(url.pathname)) return null
    if (url.pathname.split('/').some((segment) => /[\\/\x00-\x20]/.test(decodeURIComponent(segment)))) return null
    return url.pathname + url.search + url.hash
  } catch { return null }
}

export function rememberPortalNext(value: unknown): void {
  const next = validPortalNext(value)
  if (next) {
    sessionStorage.removeItem('faros.app-access-next')
    sessionStorage.setItem(KEY, next)
  }
}

export function consumePortalNext(): string | null {
  const value = sessionStorage.getItem(KEY)
  sessionStorage.removeItem(KEY)
  return validPortalNext(value)
}
