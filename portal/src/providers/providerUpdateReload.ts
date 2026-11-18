// A provider deploy never interrupts a mounted provider page: the document
// keeps running the bundle it loaded. When the user next opens a provider
// whose version changed since this document loaded it, the frame does an
// ordinary full page load of the URL they are already navigating to, which
// fetches the new bundle into a fresh document.
//
// The guard stops that from looping. A fresh document holds no provider
// bundle, so a second "version changed" for the same provider within the
// window means the catalog itself is flapping (for example, hub replicas
// disagreeing mid-rollout); the frame then shows its reload error instead of
// reloading again. Without session storage there is no guard, so no reload.

const KEY = 'railgrid:provider-update-reload'
export const PROVIDER_UPDATE_RELOAD_WINDOW_MS = 60_000

interface ReloadMarker {
  name: string
  at: number
}

export interface ProviderUpdateReloadEnv {
  storage?: Pick<Storage, 'getItem' | 'setItem'>
  reload?: () => void
  now?: number
}

/** Returns true when a page load was started for the provider's new version. */
export function reloadForProviderUpdate(name: string, env: ProviderUpdateReloadEnv = {}): boolean {
  const now = env.now ?? Date.now()
  try {
    const storage = env.storage ?? window.sessionStorage
    const previous = readMarker(storage.getItem(KEY))
    if (previous?.name === name && typeof previous.at === 'number' &&
        now - previous.at >= 0 && now - previous.at < PROVIDER_UPDATE_RELOAD_WINDOW_MS) {
      return false
    }
    storage.setItem(KEY, JSON.stringify({ name, at: now } satisfies ReloadMarker))
  } catch {
    return false
  }
  ;(env.reload ?? (() => window.location.reload()))()
  return true
}

// A marker this code cannot read is treated as absent, never as a block.
function readMarker(raw: string | null): Partial<ReloadMarker> | null {
  if (!raw) return null
  try {
    const parsed: unknown = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed as Partial<ReloadMarker> : null
  } catch {
    return null
  }
}
