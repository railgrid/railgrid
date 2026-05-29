// Custom-element shell for the server-edges provider. Mirrors
// kubernetes-edges' element.ts; the per-provider isolation is
// deliberate so each Vue runtime + Pinia + router stays scoped to its
// own bundle.

import { createApp, h, reactive, type App } from 'vue'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

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
  private router: Router | null = null
  private state: ElementState = reactive({ context: null, subPath: '' })

  set kedgeContext(v: KedgeContext) {
    this.state.context = v
    const next = computeSubPath(v?.basePath)
    if (next === this.state.subPath) return
    this.state.subPath = next
    // Drive the internal memory-history router from portal-side
    // navigation. afterEach below skips re-emit when paths already
    // match, so this stays a one-way push.
    const target = '/' + next.replace(/^\//, '')
    if (this.router && this.router.currentRoute.value.path !== target) {
      this.router.replace(target)
    }
  }
  get kedgeContext(): KedgeContext | null {
    return this.state.context
  }

  connectedCallback() {
    if (this.app) return

    this.router = createInternalRouter('/' + this.state.subPath.replace(/^\//, ''))
    const router = this.router

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
    this.router = null
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
