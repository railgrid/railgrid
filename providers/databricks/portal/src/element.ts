import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import type { FarosContext } from './types'

export class DatabricksElement extends HTMLElement {
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
    this._host.className = 'databricks-host'
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
