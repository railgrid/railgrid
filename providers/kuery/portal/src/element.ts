import type { ProviderFetch } from './portalkit/tenant'

import { createApp, reactive, type App as VueApp } from 'vue'

import App from './App.vue'

export interface RailgridContext {
  // fetch is the host-owned transport: it injects Authorization and the
  // tenant headers and refuses paths outside this provider's allow list.
  // Send every hub request through portalkit providerFetch(ctx).
  fetch?: ProviderFetch | null
  /** @deprecated Read-only fallback for older hosts; use fetch. */
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

export class KueryElement extends HTMLElement {
  private readonly state = reactive<{ context: RailgridContext | null }>({ context: null })
  private app: VueApp | null = null

  set railgridContext(value: RailgridContext | null) { this.state.context = value }
  get railgridContext(): RailgridContext | null { return this.state.context }

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
