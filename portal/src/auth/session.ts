// Single source of truth for "the session is dead — bounce to /login".
//
// Before this module the detection was scattered: every REST `fetch` in
// the stores re-implemented its own `if (!res.ok)` check and silently
// swallowed an expired token (the user kept seeing errors instead of being
// sent to login). Now every path funnels through here:
//   - REST callers use authFetch(), which injects the bearer and fires
//     notifySessionExpired() on a 401.
//   - The auth store calls notifySessionExpired() directly.
//
// The redirect itself lives in the shell (App.vue), reached via a window
// event. We deliberately do NOT import `@/router` or any Pinia store
// here: this module is pulled into provider micro-frontend bundles via
// the `@faros-edges` alias, and a static router/store import drags the
// entire portal SPA into each provider's IIFE. Depending only on
// `@/auth/token` (pure functions) and the DOM keeps this leaf-level and
// cycle-free.
import { readTenant } from '@/portalkit/tenant'
import { loadAuth, isExpired, refreshToken } from '@/auth/token'

// Window event the shell listens for to drop a dead session and redirect
// to /login (see portal/src/App.vue). No-op inside provider
// micro-frontends, which register no listener.
export const SESSION_EXPIRED_EVENT = 'faros-session-expired'

// One page load can fan out a dozen authenticated requests (provider
// list + admin probe + N workspace reads). When the token dies they all
// come back 401 at once; without this latch each one would fire the event
// and race a separate logout()+router.replace(). Latch until the next
// successful login re-arms it via resetSessionExpired().
let notified = false

// notifySessionExpired signals the shell that the persisted session can
// no longer authenticate the user, exactly once per dead session.
export function notifySessionExpired(): void {
  if (notified) return
  notified = true
  window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT))
}

// resetSessionExpired re-arms the latch. Call it on a successful login so
// a later expiry triggers the redirect again.
export function resetSessionExpired(): void {
  notified = false
}

// getBearerToken returns a usable bearer, refreshing an expired one
// in-place. Returns null when there is no session, or when an expired
// token can't be refreshed — in the latter case it also fires
// notifySessionExpired(), so callers don't have to detect dead sessions
// themselves. This is the store-free core; useAuthStore.getValidToken()
// wraps it to also sync the reactive token ref.
export async function getBearerToken(): Promise<string | null> {
  const stored = loadAuth()
  if (!stored) return null
  if (!isExpired(stored)) return stored.idToken

  const refreshed = await refreshToken(stored)
  if (refreshed) return refreshed.idToken

  // Expired and unrefreshable: the session is dead.
  notifySessionExpired()
  return null
}

interface AuthHeaderOptions {
  // tenant: include X-Faros-Org / X-Faros-Workspace from the sidebar
  // selection so workspace-scoped hub endpoints (/api/orgs/.../providers)
  // target the workspace the user is viewing. The hub re-verifies these
  // against the caller's membership, so they can't be spoofed.
  tenant?: boolean
}

function tenantHeaders(): Record<string, string> {
  const h: Record<string, string> = {}
  const t = readTenant()
  if (t.orgUUID) h['X-Faros-Org'] = t.orgUUID
  if (t.workspaceUUID) h['X-Faros-Workspace'] = t.workspaceUUID
  return h
}

// authFetch is the single entrypoint for authenticated REST calls to the
// hub. It injects a refreshed bearer (and, when opts.tenant is set, the
// org/workspace headers) and centrally detects a dead session: a 401
// means the token was rejected, so it fires notifySessionExpired() before
// returning the response. Callers keep their own `if (!res.ok)` handling
// for endpoint-specific errors.
//
// 403 and 404 are intentionally NOT treated as session failures here:
// for an authenticated user they're legitimate authorization / not-found
// answers (e.g. /api/admin/* returns 403 to a non-admin), and logging the
// user out on them would be wrong.
export async function authFetch(
  path: string,
  opts: RequestInit & AuthHeaderOptions = {},
): Promise<Response> {
  const { tenant, headers, ...init } = opts
  const selectedHeaders = tenant ? tenantHeaders() : undefined
  const token = await getBearerToken()

  // Build via the Headers API so every HeadersInit shape a caller might
  // pass (plain object, [name,value][], or a Headers instance) is merged
  // correctly — a plain spread/cast would silently drop the latter two.
  // Order matters: tenant headers first, then caller headers (so a caller
  // can override them), then Authorization last so it always wins.
  const merged = new Headers(selectedHeaders)
  if (headers) new Headers(headers).forEach((value, key) => merged.set(key, value))
  if (token) merged.set('Authorization', `Bearer ${token}`)

  const res = await fetch(path, {
    credentials: 'same-origin',
    ...init,
    headers: merged,
  })

  if (res.status === 401) notifySessionExpired()
  return res
}
