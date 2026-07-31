// AgentsElement is the custom element the kedge portal renders for the agents
// provider. The portal loads main.js (registering this element), appends the
// element, and sets element.kedgeContext as a JS property — no iframe, no
// postMessage. The element runs in light DOM so the portal's CSS variables
// cascade in.
//
// Information architecture: four tabs — Agents · Activity · Connections ·
// Models. Agents own their schedules, triggers, channels and tools (edited in
// the agent's Config pane, next to a live chat playground); Activity is the run
// feed and trace viewer, with pending approvals pinned on top; Connections
// carries Toolsets as a section; Models is the usage dashboard + credentials.
//
// This element is a thin shell: it owns the KedgeContext, one ApiClient, one
// AppStore, the current Route, and the render dispatch.

import { html, nothing, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { ApiClient } from './api'
import { AppStore } from './store'
import { LightElement } from './ui/base'
import { icon, type IconName } from './ui/icon'
import { clearToasts } from './ui/toast'
import type { KedgeContext } from './types'
import { DEFAULT_ROUTE, MENUS, activeMenu, parseHash, syncHash, type MenuKey, type Route } from './router'

import './ui/toast'
import './views/agents-list'
import './views/agent-detail'
import './views/activity'
import './views/run-detail'
import './views/connections'
import './views/models'

const MENU_META: Record<MenuKey, { icon: IconName; label: string }> = {
  agents: { icon: 'bot', label: 'Agents' },
  activity: { icon: 'gauge', label: 'Activity' },
  connections: { icon: 'plug', label: 'Connections' },
  models: { icon: 'cpu', label: 'Models' },
}

export class AgentsElement extends LightElement {
  @state() private ctx: KedgeContext | null = null
  @state() private route: Route = DEFAULT_ROUTE

  private api = new ApiClient()
  private store = new AppStore(this.api)
  private loadedTenant: string | null = null

  set kedgeContext(v: KedgeContext | null) {
    this.ctx = v
    this.api.setContext(v)
    this.maybeLoad()
  }
  get kedgeContext(): KedgeContext | null {
    return this.ctx
  }

  private onHashChange = (): void => {
    this.route = parseHash()
  }
  private onStoreChange = (): void => this.requestUpdate()
  private onNavigate = (e: Event): void => {
    this.go((e as CustomEvent<Route>).detail)
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.route = parseHash()
    window.addEventListener('hashchange', this.onHashChange)
    this.addEventListener('agents-navigate', this.onNavigate)
    this.store.addEventListener('change', this.onStoreChange)
    this.maybeLoad()
  }

  disconnectedCallback(): void {
    window.removeEventListener('hashchange', this.onHashChange)
    this.removeEventListener('agents-navigate', this.onNavigate)
    this.store.removeEventListener('change', this.onStoreChange)
    this.store.disconnect()
    super.disconnectedCallback()
  }

  private go(r: Route): void {
    this.route = r
    syncHash(r)
  }

  private maybeLoad(): void {
    if (!this.ctx?.basePath || !this.api.hasWorkspace()) return
    const key = this.api.tenantKey()
    if (key === this.loadedTenant) return
    // Switching tenants (not the first load) resets to the Agents tab so we
    // never show a stale agent from another workspace. On first load we keep
    // the hash-restored route so a refresh stays put. Component state dies with
    // the components themselves — no manual reset choreography needed.
    if (this.loadedTenant !== null) {
      clearToasts()
      this.go(DEFAULT_ROUTE)
    }
    this.loadedTenant = key
    // A fresh store drops every slice, so views can't render another
    // workspace's rows for a frame.
    this.store.disconnect()
    this.store.removeEventListener('change', this.onStoreChange)
    this.store = new AppStore(this.api)
    this.store.addEventListener('change', this.onStoreChange)
    this.store.loadAll()
    this.store.connect()
    this.requestUpdate()
  }

  render(): TemplateResult {
    if (!this.ctx) return html`<div class="agents-empty"><p class="muted">Connecting…</p></div>`
    if (!this.api.hasWorkspace()) {
      return html`<div class="agents-empty">
        <p class="muted">Select an organization and workspace in the sidebar to use your agents.</p>
      </div>`
    }
    syncHash(this.route)
    return html`
      <div class="agents-app">
        ${this.renderNav()}
        <div class="agents-view">${this.renderRoute()}</div>
      </div>
      <agents-toasts></agents-toasts>
    `
  }

  private renderRoute(): TemplateResult {
    const bind = { store: this.store, api: this.api }
    switch (this.route.kind) {
      case 'agent':
        return html`<agents-agent-detail
          .store=${bind.store}
          .api=${bind.api}
          .name=${this.route.name}
          .tab=${this.route.tab}
        ></agents-agent-detail>`
      case 'run':
        return html`<agents-run-detail .store=${bind.store} .api=${bind.api} .runID=${this.route.id}></agents-run-detail>`
      default:
        switch (this.route.menu) {
          case 'agents':
            return html`<agents-agents-list .store=${bind.store} .api=${bind.api}></agents-agents-list>`
          case 'activity':
            return html`<agents-activity .store=${bind.store} .api=${bind.api}></agents-activity>`
          case 'connections':
            return html`<agents-connections .store=${bind.store} .api=${bind.api}></agents-connections>`
          case 'models':
            return html`<agents-models .store=${bind.store} .api=${bind.api}></agents-models>`
        }
    }
  }

  private renderNav(): TemplateResult {
    const active = activeMenu(this.route)
    const counts: Record<MenuKey, number> = {
      agents: this.store.agents.data.length,
      activity: this.store.pendingInbox().length,
      connections: this.store.connections.data.length + this.store.toolsets.data.length,
      models: this.store.credentials.data.length,
    }
    return html`<nav class="agents-nav" aria-label="Agents provider sections">
      ${MENUS.map((m) => {
        const meta = MENU_META[m]
        const n = counts[m]
        const isActive = m === active
        return html`<button
          class="agents-navtab ${isActive ? 'sel' : ''}"
          aria-current=${isActive ? 'page' : nothing}
          @click=${() => this.go({ kind: 'menu', menu: m })}
        >
          ${icon(meta.icon)} ${meta.label}
          ${n ? html`<span class="agents-navcount ${m === 'activity' ? 'attn' : ''}">${n}</span>` : nothing}
        </button>`
      })}
      ${this.store.live
        ? nothing
        : html`<span class="agents-offline" title="Live updates are reconnecting; falling back to polling."
            >${icon('refresh')} reconnecting</span
          >`}
    </nav>`
  }
}
