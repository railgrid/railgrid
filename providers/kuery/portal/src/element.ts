import { createApp, reactive, type App as VueApp } from 'vue'

import App from './App.vue'

export interface FarosContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

export class KueryElement extends HTMLElement {
  private readonly state = reactive<{ context: FarosContext | null }>({ context: null })
  private app: VueApp | null = null

  set farosContext(value: FarosContext | null) { this.state.context = value }
  get farosContext(): FarosContext | null { return this.state.context }

  connectedCallback(): void {
    if (this.app) return
    this.app = createApp(App, { state: this.state })
    this.app.mount(this)
  }

  disconnectedCallback(): void {
    this.app?.unmount()
    this.app = null
  }
}
