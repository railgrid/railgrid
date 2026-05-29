// Custom-element shell for the kubernetes-edges dashboard tile. Mirror
// of element.ts but smaller: no internal router, just a Vue app
// rendering DashboardTile.vue. The portal's DashboardTile.vue mounts
// this element on the dashboard page, sets kedgeContext, and listens
// for kedge-navigate CustomEvents to drive vue-router pushes.

import { createApp, h, reactive, type App } from 'vue'
import { createPinia } from 'pinia'

import DashboardTile from './DashboardTile.vue'
import type { KedgeContext } from './auth-adapter'

const TAG = 'kedge-dashboard-tile-kubernetes-edges'

interface ElementState {
  context: KedgeContext | null
}

class KedgeDashboardTileKubernetesEdges extends HTMLElement {
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
    // provide() so DashboardTile.vue can `inject('dispatchNavigate')`
    // without threading a callback prop down through templates.
    this.app.provide('dispatchNavigate', dispatch)
    this.app.mount(this)
  }

  disconnectedCallback() {
    this.app?.unmount()
    this.app = null
  }
}

if (!customElements.get(TAG)) {
  customElements.define(TAG, KedgeDashboardTileKubernetesEdges)
}
