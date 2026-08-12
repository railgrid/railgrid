// Private published apps bounce an unauthenticated browser to
// `/login?next=/auth/apps/authorize?...`. The login flow (token or OIDC)
// stashes that hub-relative continuation here and resumes it once the hub
// browser session exists. Only the exact published-app authorize path is
// ever honored, so the login page cannot be used as an open redirector.
const STORAGE_KEY = 'faros.app-access-next'
const AUTHORIZE_PATTERN = /^\/auth\/apps\/authorize\?/

export function rememberAppAccessNext(next: string | null): void {
  if (next && AUTHORIZE_PATTERN.test(next)) {
    sessionStorage.setItem(STORAGE_KEY, next)
  }
}

/**
 * Returns the pending authorize continuation (removing it), or null.
 * Callers navigate with window.location.assign — the target is a hub route,
 * not an SPA route.
 */
export function consumeAppAccessNext(): string | null {
  const next = sessionStorage.getItem(STORAGE_KEY)
  if (next) sessionStorage.removeItem(STORAGE_KEY)
  return next && AUTHORIZE_PATTERN.test(next) ? next : null
}
