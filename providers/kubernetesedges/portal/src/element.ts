// Custom-element shell for the kubernetes-edges provider. Mirrors the
// mcp provider's element.ts; the per-provider isolation is deliberate
// because each provider is its own micro-frontend bundle with its own
// Vue runtime, Pinia instance, and router.

import { createApp, h, reactive, type App } from 'vue'
import { createPinia } from 'pinia'
import type { Router } from 'vue-router'

import KubernetesEdgesHost from './KubernetesEdgesHost.vue'
import { createInternalRouter } from './router'
import type { KedgeContext } from './auth-adapter'

const TAG = 'kedge-provider-kubernetes-edges'

interface ElementState {
  context: KedgeContext | null
  subPath: string
}

class KedgeProviderKubernetesEdges extends HTMLElement {
  private app: App | null = null
  private router: Router | null = null
  private state: ElementState = reactive({ context: null, subPath: '' })

  set kedgeContext(v: KedgeContext) {
    this.state.context = v
    const next = computeSubPath(v?.basePath)
    if (next === this.state.subPath) return
    this.state.subPath = next
    // Drive the internal memory-history router from portal-side
    // navigation (side-nav clicks, browser back/forward). The afterEach
    // guard below filters the re-entry: when paths already match it
    // skips the kedge-navigate dispatch, so this won't loop.
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
      render: () => h(KubernetesEdgesHost, { context: this.state.context, subPath: this.state.subPath }),
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
  customElements.define(TAG, KedgeProviderKubernetesEdges)
}
