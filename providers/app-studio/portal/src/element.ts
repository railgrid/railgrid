import type { FarosContext } from './types'
import {
  installCurrentAppStudioLazyLoaders,
  loadCurrentAppStudioSurface,
  type LazySurface,
} from './lazyLoaderRegistry'

const TAG = 'faros-provider-app-studio'
const TILE_TAG = 'faros-dashboard-tile-app-studio'
const PROVIDER_BOOTSTRAP_RETRY_EVENT = 'faros-provider-bootstrap-retry'

interface LazyMount {
  setContext(context: FarosContext | null): void
  unmount(): void
}

type MountModule = {
  mount(element: HTMLElement, context: FarosContext | null): LazyMount
}

function loadCurrentMount(surface: LazySurface): Promise<MountModule> {
  return loadCurrentAppStudioSurface<MountModule>(globalThis, surface)
}

abstract class LazyAppStudioElement extends HTMLElement {
  private context: FarosContext | null = null
  private generation = 0
  private mountHandle: LazyMount | null = null

  protected abstract loadMount(): Promise<MountModule>

  set farosContext(value: FarosContext | null) {
    this.context = value
    this.mountHandle?.setContext(value)
  }

  get farosContext(): FarosContext | null {
    return this.context
  }

  connectedCallback(): void {
    if (this.mountHandle) return
    this.startLoad()
  }

  private startLoad(): void {
    if (!this.isConnected) return
    const generation = ++this.generation
    const status = document.createElement('p')
    status.className = 'k-loading-reveal'
    Object.assign(status.style, {
      margin: '0',
      padding: '16px',
      color: 'var(--color-text-muted, #8587a1)',
      fontSize: '14px',
    })
    status.setAttribute('role', 'status')
    status.setAttribute('aria-live', 'polite')
    status.textContent = 'Loading App Studio…'
    this.replaceChildren(status)
    void this.loadMount()
      .then(({ mount }) => {
        if (generation !== this.generation || !this.isConnected) return
        this.mountHandle = mount(this, this.context)
      })
      .catch(() => {
        if (generation !== this.generation || !this.isConnected) return
        const error = document.createElement('div')
        Object.assign(error.style, {
          display: 'grid',
          gap: '12px',
          padding: '16px',
          color: 'var(--color-danger, #ff5d5d)',
          fontSize: '14px',
        })
        error.setAttribute('role', 'alert')
        error.setAttribute('aria-live', 'assertive')
        const message = document.createElement('p')
        message.textContent = 'App Studio could not be loaded.'
        const retry = document.createElement('button')
        retry.type = 'button'
        // The page and tile styles are intentionally lazy. Keep this failure
        // action usable before either bundle has loaded by relying on the
        // host-owned button primitive plus intrinsic touch-safe dimensions.
        retry.className = 'k-btn k-btn--danger'
        Object.assign(retry.style, {
          width: 'fit-content',
          minHeight: '44px',
        })
        retry.textContent = 'Retry loading App Studio'
        retry.addEventListener('click', () => {
          // A failed dynamic import can point at a hashed chunk retired by a
          // same-version deployment. Retrying that captured loader cannot
          // recover; ask the host to invalidate and reload main.js so this
          // stable wrapper receives the deployment's current lazy loaders.
          const handled = !this.dispatchEvent(new CustomEvent(PROVIDER_BOOTSTRAP_RETRY_EVENT, {
            bubbles: true,
            composed: true,
            cancelable: true,
            detail: { providerName: 'app-studio' },
          }))
          // Direct and legacy embeddings have no host loader. Preserve their
          // local retry behavior for transient failures.
          if (!handled) this.startLoad()
        })
        error.append(message, retry)
        this.replaceChildren(error)
      })
  }

  disconnectedCallback(): void {
    this.generation++
    this.mountHandle?.unmount()
    this.mountHandle = null
    this.replaceChildren()
  }
}

class ProjectsElement extends LazyAppStudioElement {
  protected loadMount(): Promise<MountModule> {
    return loadCurrentMount('page')
  }
}

class AppStudioDashboardTileElement extends LazyAppStudioElement {
  protected loadMount(): Promise<MountModule> {
    return loadCurrentMount('tile')
  }
}

// ProviderFrame reloads main.js when the deployed provider version changes,
// but the browser cannot redefine an existing custom element. Keep the small
// registered wrapper and refresh its lazy loaders on every current bootstrap.
// The generation check is inside this side-effect boundary because removing a
// prepared classic script does not prevent its body from executing later.
export function registerAppStudioElements(bootstrapGeneration: string | undefined): boolean {
  const installed = installCurrentAppStudioLazyLoaders<MountModule>(globalThis, bootstrapGeneration, {
    page: () => import('./page-element'),
    tile: () => import('./tile-element'),
  })
  if (!installed) return false

  if (!customElements.get(TAG)) {
    customElements.define(TAG, ProjectsElement)
  }

  if (!customElements.get(TILE_TAG)) {
    customElements.define(TILE_TAG, AppStudioDashboardTileElement)
  }
  return true
}
