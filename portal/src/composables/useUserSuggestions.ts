// useUserSuggestions backs the add-member box with GET /api/users/search.
//
// The hub rate-limits that endpoint hard per caller (a small burst, then one
// request every few seconds), so this spends requests carefully:
//   - nothing is sent below USER_SEARCH_MIN_QUERY characters (the hub
//     refuses those anyway) or for input with spaces, and a static-token
//     member ID ("railgrid:static:<hash>") waits for that many hash characters,
//     which is when the hub starts matching static-token accounts;
//   - requests are debounced while the person types;
//   - an answer with fewer than the hub's cap is complete for its prefix, so
//     every longer query under it is answered by filtering locally;
//   - after a 429 nothing is sent until Retry-After has passed.
// Typing one full address therefore usually costs one request.

import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { authFetch } from '@/auth/session'
import { authSessionRevision } from '@/auth/token'

export interface UserSuggestion {
  user: string
  // What goes in the add box: the email, or the member ID (RBAC identity)
  // of an account without one.
  memberId: string
  email?: string
  displayName?: string
}

// Mirror restapi.UserSearchMinQuery / UserSearchMaxResults.
export const USER_SEARCH_MIN_QUERY = 5
const USER_SEARCH_MAX_RESULTS = 5
const DEBOUNCE_MS = 350

// Shared by every add-member box on the page; reset when the account changes.
const cache = new Map<string, UserSuggestion[]>()
let cacheRevision = authSessionRevision()
let blockedUntil = 0

function currentCache(): Map<string, UserSuggestion[]> {
  const revision = authSessionRevision()
  if (revision !== cacheRevision) {
    cache.clear()
    cacheRevision = revision
    blockedUntil = 0
  }
  return cache
}

// staticHash mirrors restapi.staticSearchHash: the hash part of a query
// written as a static-token member ID, or null.
function staticHash(q: string): string | null {
  const rest = q.startsWith('railgrid:') ? q.slice('railgrid:'.length) : q
  return rest.startsWith('static:') ? rest.slice('static:'.length) : null
}

// searchable mirrors when the hub answers: every query needs the minimum
// length, and a static-token member ID needs it in the hash part too (the
// hub answers shorter ones, but never with a static-token account).
function searchable(q: string): boolean {
  if ([...q].length < USER_SEARCH_MIN_QUERY || /\s/.test(q)) return false
  const hash = staticHash(q)
  return hash === null || [...hash].length >= USER_SEARCH_MIN_QUERY
}

function matches(s: UserSuggestion, q: string): boolean {
  return [s.memberId, s.email ?? '', s.displayName ?? ''].some((v) => v.toLowerCase().startsWith(q))
}

function cached(q: string): UserSuggestion[] | null {
  const c = currentCache()
  const exact = c.get(q)
  if (exact) return exact
  const wantStatic = staticHash(q) !== null
  for (let n = q.length - 1; n >= USER_SEARCH_MIN_QUERY; n--) {
    const prefix = q.slice(0, n)
    // An answer for a shorter prefix covers this query only if the hub
    // would have included the same kinds of account: "railgrid" never returns
    // static-token accounts, "railgrid:static:02d4b" does.
    if (wantStatic && !(staticHash(prefix) !== null && searchable(prefix))) continue
    const shorter = c.get(prefix)
    if (shorter && shorter.length < USER_SEARCH_MAX_RESULTS) return shorter.filter((s) => matches(s, q))
  }
  return null
}

export function useUserSuggestions(query: Ref<string>) {
  const suggestions = ref<UserSuggestion[]>([])
  let timer: ReturnType<typeof setTimeout> | undefined
  let latest = 0

  watch(query, (value) => {
    if (timer !== undefined) clearTimeout(timer)
    const request = ++latest
    const q = value.trim().toLowerCase()
    if (!searchable(q)) {
      suggestions.value = []
      return
    }
    const hit = cached(q)
    if (hit) {
      suggestions.value = hit
      return
    }
    timer = setTimeout(async () => {
      if (Date.now() < blockedUntil) return
      try {
        const resp = await authFetch(`/api/users/search?q=${encodeURIComponent(q)}`)
        if (resp.status === 429) {
          const seconds = Number(resp.headers.get('Retry-After')) || 6
          blockedUntil = Date.now() + seconds * 1000
          return
        }
        if (!resp.ok) return
        const data = (await resp.json()) as { items?: UserSuggestion[] }
        const items = data.items ?? []
        currentCache().set(q, items)
        if (request === latest) suggestions.value = items
      } catch {
        /* suggestions are optional; typing the full address still works */
      }
    }, DEBOUNCE_MS)
  })

  onBeforeUnmount(() => {
    if (timer !== undefined) clearTimeout(timer)
  })

  return { suggestions }
}
