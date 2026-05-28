// Custom-element shell for the MCP provider. The portal's ProviderFrame
// loads main.js and waits for <kedge-provider-mcp> to register; we
// register exactly once here and create a fresh isolated Vue 3 app per
// element instance.
//
// Why a fresh Vue app per instance rather than Vue's defineCustomElement?
// defineCustomElement attaches Shadow DOM by default, which would block
// the portal's :root CSS custom properties from cascading in. Light DOM
// is essential for palette parity, and we'd rather own the lifecycle
// (mount/unmount, context propagation) than fight the helper.

import { createApp, h, reactive, type App } from 'vue'
import { createPinia } from 'pinia'

import MCPHost from './MCPHost.vue'
import { createInternalRouter } from './router'
import type { KedgeContext } from './auth-adapter'

const TAG = 'kedge-provider-mcp'

// Reactive state container shared between the element and Vue. We keep
// it `reactive` (not `ref`) so the host component can read .context /
// .subPath directly through props without unwrapping.
interface ElementState {
  context: KedgeContext | null
  subPath: string
}

class KedgeProviderMCP extends HTMLElement {
  private app: App | null = null
  private state: ElementState = reactive({ context: null, subPath: '' })

  // The portal sets this AS A PROPERTY (not attribute) after appending
  // the element. We accept partial updates — theme/token rotation only
  // touches a few fields at a time.
  set kedgeContext(v: KedgeContext) {
    this.state.context = v
    // basePath is the canonical /ui/providers/mcp prefix; anything past
    // that is the in-provider sub-route. We compute it from the
    // browser location since the portal doesn't push it explicitly.
    this.state.subPath = computeSubPath(v?.basePath)
  }
  get kedgeContext(): KedgeContext | null {
    return this.state.context
  }

  connectedCallback() {
    if (this.app) return // double-mount guard for HMR

    const router = createInternalRouter('/' + this.state.subPath.replace(/^\//, ''))

    // Bubble internal navigations up to the portal's SPA router via the
    // kedge-navigate CustomEvent ProviderFrame listens for. The portal
    // pushes /providers/mcp/{path} and the URL bar updates.
    router.afterEach((to) => {
      const path = to.path === '/' ? '' : to.path.replace(/^\//, '')
      // Avoid re-emitting events from a navigation we triggered ourselves
      // in response to a portal-driven subPath change.
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
      render: () => h(MCPHost, { context: this.state.context, subPath: this.state.subPath }),
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

// computeSubPath turns /ui/providers/mcp[/...] (browser pathname) into
// the in-provider sub-route (e.g. "" or "myserver"). basePath is what
// the portal hands us — it's the /ui/providers/{name} prefix; the
// remainder of window.location.pathname is the sub-route.
function computeSubPath(basePath?: string): string {
  if (!basePath) return ''
  const p = window.location.pathname
  if (!p.startsWith(basePath)) return ''
  return p.slice(basePath.length).replace(/^\//, '')
}

if (!customElements.get(TAG)) {
  customElements.define(TAG, KedgeProviderMCP)
}
