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

// useDashboardTiles probes which providers actually ship a
// <kedge-dashboard-tile-{name}> custom element, so the dashboard can
// place ONLY real tiles in the grid.
//
// Without this, DashboardPage placed a grid cell for every ready+UI
// provider and each <DashboardTile> discovered at runtime (after a
// timeout) that its provider had no tile element — emitting `no-tile`,
// which pulled the cell back out and reflowed the grid. That
// render-then-remove was a visible flicker on every load, because the
// no-tile knowledge was session-only.
//
// Here we load each candidate's bundle once and race whenDefined for its
// tile tag against a timeout, BEFORE anything is placed. Results are
// cached in localStorage keyed by name@version, so a provider is only
// re-probed when its bundle version changes (a later version might add a
// tile). A module-level cache dedupes within a session across remounts.

import { ref } from 'vue'
import type { ProviderDTO } from '@/stores/providers'

// A bundle that ships a tile defines its element within this window of
// the script finishing; one that doesn't never will. Same-origin local
// bundles resolve in well under this — it's a ceiling, not a typical wait.
const TILE_PROBE_TIMEOUT_MS = 1500
const STORAGE_KEY = 'kedge-dashboard-tile-probe'

// key: `${name}@${version}` → whether the bundle registered a tile element.
type ProbeCache = Record<string, boolean>

const tagFor = (name: string) => `kedge-dashboard-tile-${name}`
const tileKey = (p: ProviderDTO) => `${p.name}@${p.version ?? '0'}`

function loadCache(): ProbeCache {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw) as unknown
    if (!parsed || typeof parsed !== 'object') return {}
    const out: ProbeCache = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === 'boolean') out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

function saveCache(c: ProbeCache) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(c))
  } catch {
    /* ignore quota / private-mode errors */
  }
}

// Module-level so the cache (and in-flight bundle loads) are shared across
// every component that probes within a session.
const memCache: ProbeCache = loadCache()

// loadProviderBundle injects a provider's /main.js exactly once, reusing
// the same script id ProviderFrame/DashboardTile use so we never
// double-load. customElements.define is idempotent, so a later mount is a
// no-op once this resolves.
const inflight = new Map<string, Promise<void>>()
function loadProviderBundle(name: string, version: string | undefined): Promise<void> {
  const scriptID = `kedge-provider-script-${name}`
  if (customElements.get(tagFor(name)) || document.getElementById(scriptID)) {
    return Promise.resolve()
  }
  const existing = inflight.get(name)
  if (existing) return existing
  const v = encodeURIComponent(version ?? '0')
  const p = new Promise<void>((resolve, reject) => {
    const s = document.createElement('script')
    s.id = scriptID
    s.src = `/ui/providers/${name}/main.js?v=${v}`
    s.async = true
    s.onload = () => resolve()
    s.onerror = () => reject(new Error(`failed to load /ui/providers/${name}/main.js`))
    document.head.appendChild(s)
  }).finally(() => inflight.delete(name))
  inflight.set(name, p)
  return p
}

// probeOne resolves true if the provider ships a tile element. Throws only
// when the bundle itself fails to load (caller treats that as "unknown,
// don't cache" so a transient failure is retried next visit).
async function probeOne(p: ProviderDTO): Promise<boolean> {
  const tag = tagFor(p.name)
  if (customElements.get(tag)) return true
  await loadProviderBundle(p.name, p.version)
  return Promise.race([
    customElements.whenDefined(tag).then(() => true),
    new Promise<boolean>((resolve) => setTimeout(() => resolve(false), TILE_PROBE_TIMEOUT_MS)),
  ])
}

export function useDashboardTiles() {
  // True while a probe round is running. DashboardPage holds the loading
  // shimmer until the FIRST round settles so the grid renders once, fully
  // resolved, instead of popping tiles in.
  const probing = ref(false)
  const probedOnce = ref(false)
  // Reactive view of the cache so `has()` re-evaluates as results land.
  const hasTile = ref<ProbeCache>({ ...memCache })

  // probe resolves tile presence for every provider in `providers`,
  // hitting the cache first and only loading bundles for unknowns. It
  // batches the reactive update so the grid sees one settled result set.
  async function probe(providers: ProviderDTO[]): Promise<void> {
    probing.value = true
    try {
      const results = await Promise.all(
        providers.map(async (p) => {
          const key = tileKey(p)
          if (key in memCache) return { key, has: memCache[key], cache: true }
          try {
            return { key, has: await probeOne(p), cache: true }
          } catch {
            // Bundle failed to load — unknown, don't poison the cache.
            return { key, has: false, cache: false }
          }
        }),
      )
      for (const r of results) {
        if (r.cache) memCache[r.key] = r.has
      }
      hasTile.value = { ...memCache }
      saveCache(memCache)
    } finally {
      probing.value = false
      probedOnce.value = true
    }
  }

  const has = (p: ProviderDTO): boolean => hasTile.value[tileKey(p)] === true

  return { probing, probedOnce, probe, has }
}
