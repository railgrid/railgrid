import { createApp, h, reactive, type App as VueApp, type ComponentPublicInstance } from 'vue'
import App from './App.vue'
import DashboardTile from './views/DashboardTile.vue'
import type { ApiClient } from './api'
import type { Route } from './router'
import type { AppStore } from './store'
import type { FarosContext } from './types'

type AgentsExposed = ComponentPublicInstance & {
  api?: ApiClient
  store?: AppStore
  route?: Route
  authorityEpoch?: number
  createSession?: number
  applyContext?: (context: FarosContext | null) => void
}

/**
 * The two public custom elements are the host/provider boundary. Everything
 * inside them is Vue-native and renders into light DOM so the host's PortalKit
 * tokens continue to cascade into the provider.
 */
export class AgentsElement extends HTMLElement {
  private readonly state = reactive<{ context: FarosContext | null }>({ context: null })
  private app: VueApp | null = null
  private instance: AgentsExposed | null = null
  private mountPoint: HTMLDivElement | null = null

  set farosContext(value: FarosContext | null) {
    // Authority rotation must happen in the setter's call stack. A later Vue
    // prop flush is too late for confirmation and create-session race fences.
    this.instance?.applyContext?.(value)
    this.state.context = value
  }

  get farosContext(): FarosContext | null {
    return this.state.context
  }

  get api(): ApiClient | undefined { return this.exposed('api') as ApiClient | undefined }
  get store(): AppStore | undefined { return this.exposed('store') as AppStore | undefined }
  get route(): Route | undefined { return this.exposed('route') as Route | undefined }
  get authorityEpoch(): number | undefined { return this.exposed('authorityEpoch') as number | undefined }
  get createSession(): number | undefined { return this.exposed('createSession') as number | undefined }

  connectedCallback(): void {
    if (this.app) return
    this.mountPoint = document.createElement('div')
    this.mountPoint.className = 'agents-vue-root'
    this.appendChild(this.mountPoint)
    this.app = createApp({
      render: () => h(App, {
        ref: (instance: unknown) => { this.instance = instance as AgentsExposed | null },
        ctx: this.state.context,
        host: this,
      }),
    })
    this.app.mount(this.mountPoint)
  }

  disconnectedCallback(): void {
    this.app?.unmount()
    this.app = null
    this.instance = null
    this.mountPoint?.remove()
    this.mountPoint = null
  }

  private exposed(key: keyof AgentsExposed): unknown {
    const value = this.instance?.[key]
    return value && typeof value === 'object' && 'value' in value
      ? (value as { value: unknown }).value
      : value
  }
}

type TileExposed = ComponentPublicInstance & {
  api?: ApiClient
  load?: () => Promise<void>
  applyContext?: (context: FarosContext | null) => void
}

export class AgentsDashboardTileElement extends HTMLElement {
  private readonly state = reactive<{ context: FarosContext | null }>({ context: null })
  private app: VueApp | null = null
  private instance: TileExposed | null = null
  private mountPoint: HTMLDivElement | null = null

  set farosContext(value: FarosContext | null) {
    // Fence an in-flight tile refresh in the same call stack as a host
    // authority change; the prop watcher runs later in Vue's scheduler.
    this.instance?.applyContext?.(value)
    this.state.context = value
  }
  get farosContext(): FarosContext | null { return this.state.context }
  get api(): ApiClient | undefined { return this.instance?.api }
  load(): Promise<void> { return this.instance?.load?.() ?? Promise.resolve() }

  connectedCallback(): void {
    if (this.app) return
    this.mountPoint = document.createElement('div')
    this.mountPoint.className = 'agents-tile-vue-root'
    this.appendChild(this.mountPoint)
    this.app = createApp({
      render: () => h(DashboardTile, {
        ref: (instance: unknown) => { this.instance = instance as TileExposed | null },
        context: this.state.context,
        onNavigate: (path: string) => this.dispatchEvent(new CustomEvent('faros-navigate', {
          detail: { provider: 'agents', path },
          bubbles: true,
          composed: true,
        })),
      }),
    })
    this.app.mount(this.mountPoint)
  }

  disconnectedCallback(): void {
    this.app?.unmount()
    this.app = null
    this.instance = null
    this.mountPoint?.remove()
    this.mountPoint = null
  }
}
