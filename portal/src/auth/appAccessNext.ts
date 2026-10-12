// Application access and identity sign-ins bounce an unauthenticated browser to
// `/ui/login?next=/auth/apps/{authorize,identity/authorize}?...`. The login flow
// (token or OIDC) stashes that hub-relative continuation here and resumes it
// once the hub browser session exists. Only these exact hub routes are ever
// honored, so the login page cannot be used as an open redirector.
const STORAGE_KEY = 'railgrid.app-access-next'
const AUTHORIZE_PREFIXES = [
  '/auth/apps/authorize?',
  '/auth/apps/identity/authorize?',
]

function validAppAccessNext(next: unknown): next is string {
  return typeof next === 'string' &&
    !/[\\\x00-\x20#]/.test(next) &&
    AUTHORIZE_PREFIXES.some((prefix) => next.startsWith(prefix))
}

export function rememberAppAccessNext(next: unknown): void {
  if (validAppAccessNext(next)) {
    sessionStorage.removeItem('railgrid.portal-next')
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
  return validAppAccessNext(next) ? next : null
}
