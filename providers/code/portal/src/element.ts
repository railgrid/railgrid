// CodeElement is the custom element the kedge portal renders for the code
// provider. It mounts a Vue 3 app in its own light-DOM container and survives
// portal re-renders by keeping a single app instance whose props are driven by
// the .kedgeContext setter. Mirrors the infrastructure provider's element.ts.

import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import type { KedgeContext } from './types'

export class CodeElement extends HTMLElement {
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
