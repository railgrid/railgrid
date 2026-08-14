// EdgesElement is the custom element the faros portal renders for the edges
// provider. It mounts a Vue 3 app in its own light-DOM container and survives
// portal re-renders by keeping a single app instance whose props are driven by
// the .farosContext setter. Mirrors the code/infrastructure providers.

import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import DashboardTile from './DashboardTile.vue'
import type { FarosContext } from './types'

export class EdgesElement extends HTMLElement {
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
    this._host.className = 'edges-host'
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

// EdgesDashboardTileElement is the console's dashboard summary card. Same
// farosContext setter contract as the page element, so the shell pushes
// context through one hook for both.
export class EdgesDashboardTileElement extends HTMLElement {
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
    this._host.className = 'edges-tile-host'
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
