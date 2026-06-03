// InfrastructureElement is the custom element the kedge portal
// renders. Mounts a Vue 3 app rooted in the element's own light-DOM
// container. The element survives portal re-renders by keeping a
// single Vue app instance whose props are driven by the
// .kedgeContext setter.

import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import DashboardTile from './DashboardTile.vue'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  // See types.ts for the routing semantics.
  subPath?: string
}

export class InfrastructureElement extends HTMLElement {
  private _vueApp: VueApp | null = null
  // Reactive container shared with the Vue app — assigning to
  // _ctx.value triggers re-renders without re-mounting.
  private _state = reactive<{ ctx: KedgeContext | null }>({ ctx: null })
  private _host: HTMLDivElement | null = null

  set kedgeContext(v: KedgeContext | null) {
    this._state.ctx = v
  }
  get kedgeContext(): KedgeContext | null {
    return this._state.ctx
  }

  connectedCallback(): void {
    if (this._vueApp) return // hot-reload safety
    this._host = document.createElement('div')
    this._host.className = 'infrastructure-host'
    this.appendChild(this._host)
    this._vueApp = createApp({
      render: () => h(App, { ctx: this._state.ctx }),
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

// InfrastructureDashboardTileElement is the per-provider tile the
// portal's <DashboardTile> component mounts on the dashboard page.
// Same kedgeContext setter contract as the page element above so the
// shell can push token / theme / basePath through the identical hook
// — only the rendered component differs.
//
// Keeping this as a separate element class (not a prop on
// InfrastructureElement) matches kubernetes-edges' tile/host split and
// lets the portal mount the tile on the dashboard without spinning up
// the full provider app.
export class InfrastructureDashboardTileElement extends HTMLElement {
  private _vueApp: VueApp | null = null
  private _state = reactive<{ ctx: KedgeContext | null }>({ ctx: null })
  private _host: HTMLDivElement | null = null

  set kedgeContext(v: KedgeContext | null) {
    this._state.ctx = v
  }
  get kedgeContext(): KedgeContext | null {
    return this._state.ctx
  }

  connectedCallback(): void {
    if (this._vueApp) return
    this._host = document.createElement('div')
    this._host.className = 'infrastructure-tile-host'
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
