// Custom-element shell for the server-edges provider. Mirrors
// kubernetes-edges' element.ts; the per-provider isolation is
// deliberate so each Vue runtime + Pinia + router stays scoped to its
// own bundle.

import { createApp, h, reactive, type App } from 'vue'
import { createPinia } from 'pinia'

import ServerEdgesHost from './ServerEdgesHost.vue'
import { createInternalRouter } from './router'
import type { KedgeContext } from './auth-adapter'

const TAG = 'kedge-provider-server-edges'

interface ElementState {
  context: KedgeContext | null
  subPath: string
}

class KedgeProviderServerEdges extends HTMLElement {
  private app: App | null = null
  private state: ElementState = reactive({ context: null, subPath: '' })

  set kedgeContext(v: KedgeContext) {
    this.state.context = v
    this.state.subPath = computeSubPath(v?.basePath)
  }
  get kedgeContext(): KedgeContext | null {
    return this.state.context
  }

  connectedCallback() {
    if (this.app) return

    const router = createInternalRouter('/' + this.state.subPath.replace(/^\//, ''))

    router.afterEach((to) => {
      const path = to.path === '/' ? '' : to.path.replace(/^\//, '')
      if (path === this.state.subPath.replace(/^\//, '')) return
      this.state.subPath = path
      this.dispatchEvent(
        new CustomEvent('kedge-navigate', {
          detail: { path },
          bubbles: true,
        }),
      )
    })

    this.app = createApp({
      render: () => h(ServerEdgesHost, { context: this.state.context, subPath: this.state.subPath }),
    })
    this.app.use(createPinia())
    this.app.use(router)
    this.app.mount(this)
  }

  disconnectedCallback() {
    this.app?.unmount()
    this.app = null
  }
}

function computeSubPath(basePath?: string): string {
  if (!basePath) return ''
  const p = window.location.pathname
  if (!p.startsWith(basePath)) return ''
  return p.slice(basePath.length).replace(/^\//, '')
}

if (!customElements.get(TAG)) {
  customElements.define(TAG, KedgeProviderServerEdges)
}
