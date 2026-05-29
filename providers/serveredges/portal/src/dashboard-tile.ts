// Custom-element shell for the server-edges dashboard tile. Mirror of
// kubernetes-edges' dashboard-tile.ts.

import { createApp, h, reactive, type App } from 'vue'
import { createPinia } from 'pinia'

import DashboardTile from './DashboardTile.vue'
import type { KedgeContext } from './auth-adapter'

const TAG = 'kedge-dashboard-tile-server-edges'

interface ElementState {
  context: KedgeContext | null
}

class KedgeDashboardTileServerEdges extends HTMLElement {
  private app: App | null = null
  private state: ElementState = reactive({ context: null })

  set kedgeContext(v: KedgeContext) {
    this.state.context = v
  }
  get kedgeContext(): KedgeContext | null {
    return this.state.context
  }

  connectedCallback() {
    if (this.app) return

    const dispatch = (path: string) => {
      this.dispatchEvent(
        new CustomEvent('kedge-navigate', {
          detail: { path: path.replace(/^\//, '') },
          bubbles: true,
        }),
      )
    }

    this.app = createApp({
      render: () => h(DashboardTile, { context: this.state.context }),
    })
    this.app.use(createPinia())
    this.app.provide('dispatchNavigate', dispatch)
    this.app.mount(this)
  }

  disconnectedCallback() {
    this.app?.unmount()
    this.app = null
  }
}

if (!customElements.get(TAG)) {
  customElements.define(TAG, KedgeDashboardTileServerEdges)
}
