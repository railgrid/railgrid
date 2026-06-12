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
// arrangement (position, size, and which tiles are hidden) and persists
// it to localStorage, keyed by workspace UUID so each workspace keeps
// its own layout. Mirrors the localStorage-only pattern used by the
// theme and dock-state stores — there is no backend sync, so a layout
// lives in one browser.
//
// The persisted layout is ADVISORY. The set of providers that may appear
// on the dashboard is decided upstream (DashboardPage gates on
// ready/hasUI/enabled); this store only remembers geometry and the
// hidden set, then reconciles that against whatever providers are live
// right now. A provider that was removed from the catalog, lost its
// binding, or turns out to expose no dashboard tile simply drops out;
// a newly-enabled provider gets appended at the bottom with a default
// size. That keeps the enablement gate authoritative and means a stale
// layout can never resurrect a tile the user can't actually open.

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

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
}

// Grid geometry constants — shared with DashboardPage so the column
// count used for placement matches the count handed to <GridLayout>.
export const GRID_COLS = 3
const DEFAULT_W = 1
const DEFAULT_H = 2

const STORAGE_PREFIX = 'kedge-dashboard-layout:'

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

function loadPersisted(ws: string): PersistedLayout {
  try {
    const raw = localStorage.getItem(storageKey(ws))
    if (!raw) return { tiles: [], hidden: [] }
    const parsed = JSON.parse(raw) as Partial<PersistedLayout>
    return {
      tiles: Array.isArray(parsed.tiles) ? parsed.tiles.filter(isTileLayout) : [],
      hidden: Array.isArray(parsed.hidden)
        ? parsed.hidden.filter((n): n is string => typeof n === 'string')
        : [],
    }
  } catch {
    return { tiles: [], hidden: [] }
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
// hidden set, and the names of the providers that are live right now, it
// produces the grid layout to render. Exported (and pure) so it can be
// unit-tested without a DOM.
//
//   geometry — known placements (later entries win, so callers can pass
//              persisted tiles followed by the in-memory working copy to
//              prefer un-saved drags).
//   hidden   — provider names the user removed.
//   names    — live candidate providers, already gated and already
//              minus any that exposed no tile this session.
//
// Returns the visible layout plus `addable`: hidden providers that are
// still live, i.e. what the "add tile" menu should offer.
export function reconcile(
  geometry: TileLayout[],
  hidden: string[],
  names: string[],
): { layout: TileLayout[]; addable: string[] } {
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
    layout.push(g ? { ...g } : { i: n, x: 0, y: 0, w: DEFAULT_W, h: DEFAULT_H })
  }

  // Append newcomers below everything else, flowing left-to-right.
  let nextY = layout.reduce((m, t) => Math.max(m, t.y + t.h), 0)
  let col = 0
  for (const n of newcomers) {
    layout.push({ i: n, x: col, y: nextY, w: DEFAULT_W, h: DEFAULT_H })
    col += 1
    if (col >= GRID_COLS) {
      col = 0
      nextY += DEFAULT_H
    }
  }

  return { layout, addable: names.filter((n) => hiddenLive.has(n)) }
}

export const useDashboardLayoutStore = defineStore('dashboardLayout', () => {
  // Active workspace key. '_' is the fallback bucket before a workspace
  // is selected, so the dashboard is still usable pre-tenant-load.
  const ws = ref<string>('_')
  // The grid's v-model. The grid library mutates these entries in place
  // as the user drags/resizes; we persist the result on layout-updated.
  const layout = ref<TileLayout[]>([])
  const hidden = ref<string[]>([])
  // Providers that loaded but registered no dashboard-tile element this
  // session. Session-only (not persisted): next reload re-probes them,
  // since a provider may ship a tile in a later version.
  const noTile = ref<Set<string>>(new Set())
  // Last candidate names handed to sync(), retained so hide/unhide/reset
  // can recompute without the caller re-passing them.
  const lastNames = ref<string[]>([])

  // addable drives the "add tile" menu: hidden providers still live.
  const addable = computed(() =>
    hidden.value.filter((n) => lastNames.value.includes(n) && !noTile.value.has(n)),
  )

  // candidate names = live, minus those that turned out to have no tile.
  function candidates(): string[] {
    return lastNames.value.filter((n) => !noTile.value.has(n))
  }

  // commit rebuilds the visible layout from current geometry + state and
  // writes it back to storage. Geometry source prefers the in-memory
  // working copy (latest drags) over what's persisted.
  function commit() {
    const persisted = loadPersisted(ws.value)
    const { layout: next } = reconcile(
      [...persisted.tiles, ...layout.value],
      hidden.value,
      candidates(),
    )
    layout.value = next
    savePersisted(ws.value, { tiles: next.map(strip), hidden: hidden.value })
  }

  // sync is called by the page whenever the workspace or the live
  // provider set changes. It loads the (possibly new) workspace's
  // persisted state and reconciles.
  function sync(workspaceUUID: string | null, names: string[]) {
    const key = workspaceUUID || '_'
    if (key !== ws.value) {
      ws.value = key
      hidden.value = loadPersisted(key).hidden
    }
    lastNames.value = names
    commit()
  }

  // persist saves the current geometry without rebuilding — used after a
  // drag/resize, where the grid already mutated `layout` in place.
  function persist() {
    savePersisted(ws.value, { tiles: layout.value.map(strip), hidden: hidden.value })
  }

  function hide(name: string) {
    if (!hidden.value.includes(name)) hidden.value = [...hidden.value, name]
    commit()
  }

  function unhide(name: string) {
    hidden.value = hidden.value.filter((n) => n !== name)
    commit()
  }

  // markNoTile is reported by a tile that loaded its bundle but found no
  // <kedge-dashboard-tile-*> element. We drop it from the grid so it
  // leaves no empty cell, without recording it as user-hidden.
  function markNoTile(name: string) {
    if (noTile.value.has(name)) return
    noTile.value = new Set(noTile.value).add(name)
    commit()
  }

  // reset clears the whole customisation for the active workspace.
  function reset() {
    hidden.value = []
    savePersisted(ws.value, { tiles: [], hidden: [] })
    commit()
  }

  return { layout, hidden, addable, sync, persist, hide, unhide, markNoTile, reset }
})
