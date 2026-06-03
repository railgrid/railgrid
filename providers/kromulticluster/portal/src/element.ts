// KroMulticlusterElement is the custom element the kedge portal
// renders. Mounts a Vue 3 app rooted in the element's own light-DOM
// container. The element survives portal re-renders by keeping a
// single Vue app instance whose props are driven by the
// .kedgeContext setter.

import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  // See types.ts for the routing semantics.
  subPath?: string
}

export class KroMulticlusterElement extends HTMLElement {
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
    this._host.className = 'kromc-host'
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
