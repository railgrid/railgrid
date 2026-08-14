// CodeElement is the custom element the faros portal renders for the code
// provider. It mounts a Vue 3 app in its own light-DOM container and survives
// portal re-renders by keeping a single app instance whose props are driven by
// the .farosContext setter. Mirrors the infrastructure provider's element.ts.

import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import DashboardTile from './DashboardTile.vue'
import type { FarosContext } from './types'

export class CodeElement extends HTMLElement {
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
    if (this._vueApp) return // hot-reload safety
    this._host = document.createElement('div')
    this._host.className = 'code-host'
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

// CodeDashboardTileElement is the summary card the console mounts on its
// dashboard page. Same farosContext setter contract as CodeElement, so the
// shell pushes context through one hook for both; only the rendered component
// differs. Kept as its own element so the dashboard never has to construct the
// full provider app to show a four-row summary.
export class CodeDashboardTileElement extends HTMLElement {
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
    this._host.className = 'code-tile-host'
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
