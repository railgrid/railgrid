import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import DashboardTile from './DashboardTile.vue'
import type { FarosContext } from './types'

const TAG = 'faros-provider-app-studio'
const TILE_TAG = 'faros-dashboard-tile-app-studio'

class ProjectsElement extends HTMLElement {
  private app: VueApp | null = null
  private host: HTMLDivElement | null = null
  private overlayRoot: HTMLDivElement | null = null
  private state = reactive<{ ctx: FarosContext | null }>({ ctx: null })

  set farosContext(v: FarosContext | null) {
    this.state.ctx = v
  }

  get farosContext(): FarosContext | null {
    return this.state.ctx
  }

  connectedCallback(): void {
    if (this.app) return
    this.style.display = 'block'
    this.style.height = '100%'
    this.style.width = '100%'
    this.style.minHeight = '0'
    this.host = document.createElement('div')
    this.host.className = 'h-full min-h-0 w-full'
    // Keep Vue Teleport targets inside the provider element. App Studio's
    // Tailwind bundle is scoped to this custom element in main.ts; a body
    // target would render the overlay markup outside that scope and lose all
    // provider utilities in the host portal.
    this.overlayRoot = document.createElement('div')
    this.overlayRoot.id = 'app-studio-overlay-root'
    this.overlayRoot.className = 'app-studio-overlay-root'
    this.appendChild(this.host)
    this.appendChild(this.overlayRoot)
    this.app = createApp({
      render: () =>
        h(App, {
          ctx: this.state.ctx,
          navigate: (path: string) => this.navigate(path),
          requestFullBleed: (fullBleed: boolean) => this.requestFullBleed(fullBleed),
        }),
    })
    this.app.mount(this.host)
  }

  disconnectedCallback(): void {
    this.app?.unmount()
    this.app = null
    if (this.host?.parentNode === this) this.removeChild(this.host)
    this.host = null
    if (this.overlayRoot?.parentNode === this) this.removeChild(this.overlayRoot)
    this.overlayRoot = null
  }

  private navigate(path: string): void {
    this.dispatchEvent(
      new CustomEvent('faros-navigate', {
        detail: { path },
        bubbles: true,
      }),
    )
  }

  private requestFullBleed(fullBleed: boolean): void {
    this.dispatchEvent(
      new CustomEvent('faros-layout-change', {
        detail: { fullBleed },
        bubbles: true,
      }),
    )
  }
}

if (!customElements.get(TAG)) {
  customElements.define(TAG, ProjectsElement)
}

// AppStudioDashboardTileElement is the console's dashboard summary card. Kept
// separate from the page element so the dashboard never constructs the full
// App Studio app — by far the heaviest provider bundle — to render four rows.
export class AppStudioDashboardTileElement extends HTMLElement {
  private _vueApp: VueApp | null = null
  private _state = reactive<{ ctx: FarosContext | null }>({ ctx: null })
  private _host: HTMLDivElement | null = null

  set farosContext(v: FarosContext | null) {
    this._state.ctx = v
  }
  get farosContext(): FarosContext | null {
    return this._state.ctx
  }

  connectedCallback(): void {
    if (this._vueApp) return
    this._host = document.createElement('div')
    this._host.className = 'app-studio-tile-host'
    this.appendChild(this._host)
    this._vueApp = createApp({
      render: () => h(DashboardTile, { context: this._state.ctx }),
    })
    this._vueApp.mount(this._host)
  }

  disconnectedCallback(): void {
    if (this._vueApp) {
      this._vueApp.unmount()
      this._vueApp = null
    }
    if (this._host && this._host.parentNode === this) {
      this.removeChild(this._host)
    }
    this._host = null
  }
}

if (!customElements.get(TILE_TAG)) {
  customElements.define(TILE_TAG, AppStudioDashboardTileElement)
}
