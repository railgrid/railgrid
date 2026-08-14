/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// dashboardLayout store: owns the user's customised dashboard tile
// arrangement (position, size, chosen column count, and which tiles are
// hidden or have no tile element) and syncs it to the hub so a layout
// follows the user across browsers and devices.
//
// Storage is two-tier:
//   - localStorage is a per-workspace OPTIMISTIC CACHE, keyed by workspace
//     UUID. It lets the grid paint instantly on load (and keeps working
//     offline) without waiting on a round-trip.
//   - the hub is the SOURCE OF TRUTH: a per-user UserPreferences CR
//     (GET/PUT /api/orgs/{org}/workspaces/{ws}/dashboard/layout). On sync
//     we render the cache immediately, then reconcile again once the
//     server answers; mutations write the cache synchronously and push a
//     debounced PUT.
//
// The persisted layout is ADVISORY. The set of providers that may appear
// on the dashboard is decided upstream (DashboardPage gates on
// ready/hasUI/enabled); this store only remembers geometry, the hidden set,
// and the column count, then reconciles that against whatever providers are live
// right now. A provider that was removed from the catalog, lost its
// binding, or exposes no dashboard tile simply drops out; a newly-enabled
// provider gets appended at the bottom with a default size.

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { authFetch } from '@/auth/session'

// A single tile's grid placement. `i` is the provider name (the grid's
// item id). x/y are column/row units; w/h are spans in those units.
export interface TileLayout {
  i: string
  x: number
  y: number
  w: number
  h: number
}

interface PersistedLayout {
  tiles: TileLayout[]
  hidden: string[]
  cols: number
}

// The hub wire shape (matches pkg/hub/restapi/preferences.go
// dashboardLayoutBody). Tiles are keyed by provider `name`, not `i`.
interface DashboardLayoutDTO {
  gridColumns: number
  tiles: { name: string; x: number; y: number; w: number; h: number }[]
  hidden: string[]
  // The hub still has a `noTile` field (pkg/hub/restapi/preferences.go) from
  // when tileless providers were dropped from the grid. They now keep their
  // card, so the console neither reads nor writes it; the field stays on the
  // wire type only so an older payload round-trips without surprises.
  noTile?: string[]
}

// Grid geometry constants. GRID_COLS is the fallback column count when the
// user has no saved preference and the caller passes none; DashboardPage
// normally hands sync() a responsive column count derived from viewport
// width. DEFAULT_W/H size a freshly-placed tile.
export const GRID_COLS = 3
const DEFAULT_W = 1
const DEFAULT_H = 2

const STORAGE_PREFIX = 'faros-dashboard-layout:'

function storageKey(ws: string): string {
  return `${STORAGE_PREFIX}${ws}`
}

function isTileLayout(t: unknown): t is TileLayout {
  if (!t || typeof t !== 'object') return false
  const o = t as Record<string, unknown>
  return (
    typeof o.i === 'string' &&
    typeof o.x === 'number' &&
    typeof o.y === 'number' &&
    typeof o.w === 'number' &&
    typeof o.h === 'number'
  )
}

function strArray(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((n): n is string => typeof n === 'string') : []
}

function loadPersisted(ws: string): PersistedLayout {
  try {
    const raw = localStorage.getItem(storageKey(ws))
    if (!raw) return { tiles: [], hidden: [], cols: 0 }
    // A payload written before tileless providers kept their card may carry a
    // `noTile` list. Ignoring it is deliberate: it used to hide those
    // providers from the dashboard with nothing in the UI to undo it.
    const parsed = JSON.parse(raw) as Partial<PersistedLayout>
    return {
      tiles: Array.isArray(parsed.tiles) ? parsed.tiles.filter(isTileLayout) : [],
      hidden: strArray(parsed.hidden),
      cols: typeof parsed.cols === 'number' ? parsed.cols : 0,
    }
  } catch {
    return { tiles: [], hidden: [], cols: 0 }
  }
}

function savePersisted(ws: string, value: PersistedLayout) {
  try {
    localStorage.setItem(storageKey(ws), JSON.stringify(value))
  } catch {
    /* ignore quota / private-mode errors */
  }
}

// Drop grid-internal bookkeeping (moved/static/…) the grid library tacks
// onto layout items, keeping only what we persist.
function strip(t: TileLayout): TileLayout {
  return { i: t.i, x: t.x, y: t.y, w: t.w, h: t.h }
}

// reconcile is the heart of the store: given remembered geometry, the
// hidden set, the live provider names, and the column count, it produces
// the grid layout to render. Exported (and pure) so it can be unit-tested
// without a DOM.
//
//   geometry — known placements (later entries win, so callers can pass
//              persisted tiles followed by the in-memory working copy to
//              prefer un-saved drags).
//   hidden   — provider names the user removed.
//   names    — live candidate providers, already gated and already minus
//              any that exposed no tile.
//   cols     — column count used to flow newcomers (and clamp x).
//
// Returns the visible layout plus `addable`: hidden providers that are
// still live, i.e. what the "add tile" menu should offer.
export function reconcile(
  geometry: TileLayout[],
  hidden: string[],
  names: string[],
  cols: number = GRID_COLS,
): { layout: TileLayout[]; addable: string[] } {
  const columns = cols > 0 ? cols : GRID_COLS
  const live = new Set(names)
  const hiddenLive = new Set(hidden.filter((n) => live.has(n)))

  // Latest-wins geometry lookup.
  const geom = new Map<string, TileLayout>()
  for (const t of geometry) geom.set(t.i, strip(t))

  // Visible = live, not hidden. Preserve order: remembered tiles first
  // (in their saved reading order), then newcomers.
  const remembered = geometry
    .map((t) => t.i)
    .filter((n, i, arr) => arr.indexOf(n) === i) // de-dup, keep first occurrence order
  const visible = remembered.filter((n) => live.has(n) && !hiddenLive.has(n))
  const visibleSet = new Set(visible)
  const newcomers = names.filter((n) => !visibleSet.has(n) && !hiddenLive.has(n))

  const layout: TileLayout[] = []
  for (const n of visible) {
    const g = geom.get(n)
    if (g) {
      // Clamp a remembered tile into the current column count so a layout
      // saved at a wider width doesn't place tiles off-grid.
      const w = Math.min(g.w, columns)
      layout.push({ ...g, w, x: Math.min(g.x, Math.max(0, columns - w)) })
    } else {
      layout.push({ i: n, x: 0, y: 0, w: DEFAULT_W, h: DEFAULT_H })
    }
  }

  // Append newcomers below everything else, flowing left-to-right.
  let nextY = layout.reduce((m, t) => Math.max(m, t.y + t.h), 0)
  let col = 0
  for (const n of newcomers) {
    layout.push({ i: n, x: col, y: nextY, w: DEFAULT_W, h: DEFAULT_H })
    col += 1
    if (col >= columns) {
      col = 0
      nextY += DEFAULT_H
    }
  }

  return { layout, addable: names.filter((n) => hiddenLive.has(n)) }
}

export const useDashboardLayoutStore = defineStore('dashboardLayout', () => {
  // Active tenant/workspace. '_' is the fallback bucket before a workspace
  // is selected, so the dashboard is still usable pre-tenant-load.
  const ws = ref<string>('_')
  const org = ref<string | null>(null)
  // The grid's v-model. The grid library mutates these entries in place
  // as the user drags/resizes; we persist the result on layout-updated.
  const layout = ref<TileLayout[]>([])
  // geometry is the AUTHORITATIVE remembered placement baseline (from the
  // hub, falling back to the localStorage cache). `layout` is derived from
  // it by reconcile against the live provider set. Keeping them separate is
  // what stops a transient default placement — produced while providers are
  // still loading — from overwriting the saved arrangement.
  const geometry = ref<TileLayout[]>([])
  const hidden = ref<string[]>([])
  // User's chosen column count. 0 = follow the caller's responsive default.
  const cols = ref<number>(0)
  // Column count actually in effect (responsive default handed by the
  // page, overridden by the user's saved `cols`). Drives reconcile.
  const effectiveCols = ref<number>(GRID_COLS)
  // Last candidate names handed to sync(), retained so hide/unhide/reset
  // can recompute without the caller re-passing them.
  const lastNames = ref<string[]>([])

  // addable drives the "add tile" menu: hidden providers still live.
  const addable = computed(() => hidden.value.filter((n) => lastNames.value.includes(n)))

  // Every live provider is a candidate. Tileless ones keep their card and
  // render a launcher body, so nothing is filtered out here.
  function candidates(): string[] {
    return lastNames.value
  }

  function snapshot(): PersistedLayout {
    return {
      tiles: geometry.value.map(strip),
      hidden: hidden.value,
      cols: cols.value,
    }
  }

  // applyLayout is a PURE derive: it recomputes the rendered `layout` from
  // the authoritative `geometry` baseline and the live candidate set. It
  // never writes back into `geometry` — so a transient render (providers
  // still loading, or a narrow viewport clamping tile widths) can't corrupt
  // or blank the remembered arrangement. Geometry only changes on real
  // events: a drag (persist), a hide/unhide (commit), or adopting the
  // cache/hub (sync/fetchRemote).
  function applyLayout() {
    const { layout: next } = reconcile(
      geometry.value,
      hidden.value,
      candidates(),
      cols.value || effectiveCols.value,
    )
    layout.value = next
  }

  // ---- hub sync ----

  function toDTO(s: PersistedLayout): DashboardLayoutDTO {
    return {
      gridColumns: s.cols,
      tiles: s.tiles.map((t) => ({ name: t.i, x: t.x, y: t.y, w: t.w, h: t.h })),
      hidden: s.hidden,
    }
  }

  function saveCache() {
    savePersisted(ws.value, snapshot())
  }

  let pushTimer: ReturnType<typeof setTimeout> | null = null
  // pushRemote writes the current snapshot to the hub, debounced so a
  // drag gesture (many layout-updated events) collapses into one PUT.
  function pushRemote() {
    if (!org.value || ws.value === '_') return
    const o = org.value
    const w = ws.value
    const body = toDTO(snapshot())
    if (pushTimer) clearTimeout(pushTimer)
    pushTimer = setTimeout(() => {
      const url = `/api/orgs/${encodeURIComponent(o)}/workspaces/${encodeURIComponent(w)}/dashboard/layout`
      void authFetch(url, {
        method: 'PUT',
        tenant: true,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }).catch(() => {
        /* best-effort; localStorage keeps the layout until next sync */
      })
    }, 400)
  }

  // fetchRemote pulls the authoritative layout for (org, ws) and adopts it
  // as the geometry baseline. Failures are silent — the cache already
  // rendered.
  //
  // Empty-response guard: the hub returns an empty layout both when the
  // user genuinely has none AND (indistinguishably) before their first
  // save. If we already hold a non-empty local geometry, we DON'T let an
  // empty server response wipe it — instead we push our local copy up, so a
  // browser that customised offline seeds the hub rather than losing work.
  async function fetchRemote(o: string, w: string) {
    const url = `/api/orgs/${encodeURIComponent(o)}/workspaces/${encodeURIComponent(w)}/dashboard/layout`
    let dto: DashboardLayoutDTO
    try {
      const res = await authFetch(url, { tenant: true })
      if (!res.ok) return
      dto = (await res.json()) as DashboardLayoutDTO
    } catch {
      return
    }
    // A workspace switch may have happened while the request was in flight.
    if (ws.value !== w) return

    const remoteTiles = (Array.isArray(dto.tiles) ? dto.tiles : []).map((t) => ({
      i: t.name,
      x: t.x,
      y: t.y,
      w: t.w,
      h: t.h,
    }))
    const remoteEmpty =
      remoteTiles.length === 0 && strArray(dto.hidden).length === 0
    if (remoteEmpty && geometry.value.length > 0) {
      // Server has nothing but we do — seed it, keep local.
      pushRemote()
      return
    }

    geometry.value = remoteTiles
    hidden.value = strArray(dto.hidden)
    cols.value = typeof dto.gridColumns === 'number' ? dto.gridColumns : 0
    applyLayout()
    saveCache()
  }

  // ---- store actions ----

  // commit re-derives the layout from the current state and writes it to
  // the cache + hub. Used by the hidden mutations. When there are
  // live candidates it captures the rendered positions as the new baseline
  // so a hide/unhide remembers where the remaining tiles sit.
  function commit() {
    applyLayout()
    if (candidates().length > 0) geometry.value = layout.value.map(strip)
    saveCache()
    pushRemote()
  }

  // sync is called by the page whenever the tenant or the live provider
  // set changes. It renders the cached layout immediately, then pulls the
  // authoritative one from the hub in the background.
  //
  //   defaultCols — responsive column count the page wants when the user
  //                 has no saved override.
  function sync(
    orgUUID: string | null,
    workspaceUUID: string | null,
    names: string[],
    defaultCols: number = GRID_COLS,
  ) {
    const key = workspaceUUID || '_'
    effectiveCols.value = defaultCols > 0 ? defaultCols : GRID_COLS
    const switched = key !== ws.value
    if (switched) {
      ws.value = key
      const cached = loadPersisted(key)
      geometry.value = cached.tiles
      hidden.value = cached.hidden
      cols.value = cached.cols
    }
    org.value = orgUUID
    lastNames.value = names

    // Render from the (cached) baseline synchronously — no round-trip, no
    // flicker. Only write the cache back once there are live candidates, so
    // an early call (providers still loading) can't blank a good cache.
    applyLayout()
    if (candidates().length > 0) saveCache()

    // Reconcile against the source of truth in the background.
    if (orgUUID && workspaceUUID) void fetchRemote(orgUUID, workspaceUUID)
  }

  // persist saves the current geometry after a drag/resize, where the grid
  // already mutated `layout` in place. Adopt that as the new baseline.
  function persist() {
    geometry.value = layout.value.map(strip)
    saveCache()
    pushRemote()
  }

  function hide(name: string) {
    if (!hidden.value.includes(name)) hidden.value = [...hidden.value, name]
    commit()
  }

  function unhide(name: string) {
    hidden.value = hidden.value.filter((n) => n !== name)
    commit()
  }

  // reset clears the whole customisation for the active workspace.
  function reset() {
    geometry.value = []
    hidden.value = []
    cols.value = 0
    savePersisted(ws.value, { tiles: [], hidden: [], cols: 0 })
    // Re-place everything at defaults and push the cleared state up.
    applyLayout()
    saveCache()
    pushRemote()
  }

  return {
    layout,
    hidden,
    addable,
    sync,
    persist,
    hide,
    unhide,
    reset,
  }
})
