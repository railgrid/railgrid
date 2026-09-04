// AgentsElement is the custom element the faros portal renders for the agents
// provider. The portal loads main.js (registering this element), appends the
// element, and sets element.farosContext as a JS property — no iframe, no
// postMessage. The element runs in light DOM so the portal's CSS variables
// cascade in.
//
// Information architecture: four tabs — Agents · Activity · Connections ·
// Models. Agents own their schedules, triggers, channels and tools (edited in
// the agent's Config pane, next to a live chat playground); Activity is the run
// feed and trace viewer, with pending approvals pinned on top; Connections
// carries Toolsets as a section; Models is the usage dashboard + credentials.
//
// This element is a thin shell: it owns the FarosContext, one ApiClient, one
// AppStore, the current Route, and the render dispatch.

import { html, nothing, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import type { DirectiveResult } from 'lit/directive.js'
import { ApiClient, type ContextAuthority } from './api'
import { AppStore } from './store'
import { LightElement } from './ui/base'
import { icon, type IconName } from './ui/icon'
import { clearToasts } from './ui/toast'
import { tabClass, tabCountClass, tabsClass } from './portalkit/tabs'
import type { Agent, Connection, Credential, FarosContext, Toolset } from './types'
import { DEFAULT_ROUTE, MENUS, activeMenu, hashFor, parseHash, syncHash, writeHash, type CreateSuccessDetail, type MenuKey, type Route } from './router'

import './views/agents-list'
import './views/agent-create'
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
  @state() private ctx: FarosContext | null = null
  @state() private route: Route = DEFAULT_ROUTE

  private api = new ApiClient()
  private store = new AppStore(this.api)
  private loadedTenant: string | null = null
  private authority: ContextAuthority | null = null
  // Every API/store rotation gets a new route generation. Keying the active
  // route surface on this value makes authority changes remount stateful views
  // (chat transcripts, config hydration, model probes) instead of rebinding a
  // live child to the new store in place.
  private authorityGeneration = 0
  // Incremented whenever a create surface is replaced or its authority is
  // rotated. Route-owned children receive this marker, so a success from a
  // detached/superseded form cannot be adopted by a later create session.
  private createSession = 0

  set farosContext(v: FarosContext | null) {
    const previous = this.authority
    const next = this.api.contextAuthority(v)
    this.ctx = v
    const changed = previous !== null && (
      previous.usable !== next.usable ||
      previous.tenantKey !== next.tenantKey ||
      previous.userKey !== next.userKey ||
      previous.token !== next.token
    )

    if (!next.usable) {
      if (changed || this.loadedTenant !== null || this.store.live) {
        this.rotateContext(v, true)
      } else {
        this.api.setContext(v)
        this.authority = next
        this.requestUpdate()
      }
      return
    }

    if (changed) {
      this.rotateContext(v, this.shouldResetRoute(previous, next))
      return
    }

    this.api.setContext(v)
    this.authority = next
    this.maybeLoad()
    this.requestUpdate()
  }
  get farosContext(): FarosContext | null {
    return this.ctx
  }

  private onHashChange = (): void => {
    this.restoreRoute()
  }
  private onPopState = (): void => {
    this.restoreRoute()
  }
  private restoreRoute(): void {
    const next = parseHash()
    this.advanceCreateSession(this.route, next)
    this.route = next
    // Keep malformed/legacy external hashes from leaving the shell rendered on
    // one route while the URL names another. Valid hashes are unchanged, so
    // this remains a no-op for ordinary back/forward traversal.
    syncHash(this.route)
    this.requestUpdate()
  }
  private onStoreChange = (): void => this.requestUpdate()
  private onNavigate = (e: Event): void => {
    if (!this.eventBelongsToCurrentStore(e)) return
    const next = (e as CustomEvent<Route>).detail
    // A create session owns one history entry. Picker/type/assisted subroutes
    // replace that entry so browser Back exits to the owning collection.
    const replaceCreateEntry = this.route.kind === 'create' && next.kind === 'create'
    this.go(next, replaceCreateEntry ? 'replace' : 'push')
  }
  private onCreateSuccess = (e: Event): void => {
    if (!this.eventBelongsToCurrentStore(e) || !this.eventBelongsToActiveCreateSession(e)) return
    const detail = (e as CustomEvent<CreateSuccessDetail>).detail
    if (!detail || this.route.kind !== 'create' || detail.resource !== this.route.resource) return
    this.adoptCreateResult(detail)
    this.go(detail.destination || this.createSuccessRoute(detail), 'replace')
  }
  private onCreateCancel = (e: Event): void => {
    if (!this.eventBelongsToCurrentStore(e) || !this.eventBelongsToActiveCreateSession(e)) return
    if (this.route.kind !== 'create') return
    this.go(this.createOwnerRoute(this.route), 'replace')
  }

  connectedCallback(): void {
    super.connectedCallback()
    const next = parseHash()
    this.advanceCreateSession(this.route, next)
    this.route = next
    // Canonicalize an unknown or legacy hash once on entry. Ordinary route
    // changes use pushState below and are never rewritten during render.
    syncHash(this.route)
    window.addEventListener('hashchange', this.onHashChange)
    window.addEventListener('popstate', this.onPopState)
    this.addEventListener('agents-navigate', this.onNavigate)
    this.addEventListener('agents-create-success', this.onCreateSuccess)
    this.addEventListener('agents-cancel', this.onCreateCancel)
    this.store.addEventListener('change', this.onStoreChange)
    this.maybeLoad()
  }

  disconnectedCallback(): void {
    window.removeEventListener('hashchange', this.onHashChange)
    window.removeEventListener('popstate', this.onPopState)
    this.removeEventListener('agents-navigate', this.onNavigate)
    this.removeEventListener('agents-create-success', this.onCreateSuccess)
    this.removeEventListener('agents-cancel', this.onCreateCancel)
    this.store.removeEventListener('change', this.onStoreChange)
    this.store.disconnect()
    super.disconnectedCallback()
  }

  private go(r: Route, mode: 'push' | 'replace' = 'push'): void {
    this.advanceCreateSession(this.route, r)
    this.route = r
    writeHash(r, mode)
    this.requestUpdate()
  }

  private maybeLoad(): void {
    const authority = this.authority || this.api.contextAuthority()
    if (!authority.usable) return
    const key = authority.tenantKey
    if (key === this.loadedTenant) return
    // Switching tenants (not the first load) resets to the Agents tab so we
    // never show a stale agent from another workspace. On first load we keep
    // the hash-restored route so a refresh stays put. Component state dies with
    // the components themselves — no manual reset choreography needed.
    if (this.loadedTenant !== null) {
      clearToasts()
      this.go(DEFAULT_ROUTE, 'replace')
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

  private rotateContext(v: FarosContext | null, resetRoute: boolean): void {
    this.store.removeEventListener('change', this.onStoreChange)
    this.store.disconnect()
    this.authorityGeneration += 1
    this.createSession += 1

    const nextApi = new ApiClient()
    nextApi.setContext(v)
    this.api = nextApi
    this.store = new AppStore(nextApi)
    this.store.addEventListener('change', this.onStoreChange)
    this.loadedTenant = null
    this.authority = nextApi.contextAuthority()
    clearToasts()
    if (resetRoute) this.go(DEFAULT_ROUTE, 'replace')
    this.maybeLoad()
    this.requestUpdate()
  }

  private shouldResetRoute(previous: ContextAuthority | null, next: ContextAuthority): boolean {
    if (!previous?.usable || previous.tenantKey !== next.tenantKey) return true
    // A route can only safely survive a token refresh when both contexts name
    // the same known subject. If either side omits a subject, treat the change
    // as a caller change rather than risk showing the prior caller's detail.
    return !(previous.userKey && next.userKey && previous.userKey === next.userKey)
  }

  private eventBelongsToCurrentStore(e: Event): boolean {
    const source = e.target as { store?: AppStore } | null
    return !source?.store || source.store === this.store
  }

  private eventBelongsToActiveCreateSession(e: Event): boolean {
    // Assisted connection setup is rendered below the route-owned connection
    // surface, so its event target does not carry the marker itself. Walk up
    // the light-DOM parent chain to find the route-owned child. This also
    // rejects a detached child that emits after a same-tick route transition.
    let source = e.target as (Node & { createSession?: number }) | null
    while (source) {
      if (typeof source.createSession === 'number') return source.createSession === this.createSession
      source = source.parentNode as (Node & { createSession?: number }) | null
    }
    return false
  }

  private advanceCreateSession(previous: Route, next: Route): void {
    if (hashFor(previous) === hashFor(next)) return
    if (previous.kind === 'create' || next.kind === 'create') this.createSession += 1
  }

  render(): TemplateResult {
    if (!this.ctx) return html`<div class="k-card agents-empty k-loading-reveal"><p class="muted" role="status">Connecting…</p></div>`
    if (!this.authority?.usable) {
      return html`<div class="k-card agents-empty">
        <p class="muted" role="status">Select an organization and workspace in the sidebar to use your agents.</p>
      </div>`
    }
    return html`
      <div class="agents-app">
        ${this.renderNav()}
        <div class="agents-view">${keyed(this.authorityGeneration, this.renderRoute())}</div>
      </div>
    `
  }

  private renderRoute(): TemplateResult | DirectiveResult {
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
      case 'create':
        return this.renderCreateRoute(this.route)
      default:
        switch (this.route.menu) {
          case 'agents':
            return html`<agents-agents-list .store=${bind.store} .api=${bind.api}></agents-agents-list>`
          case 'activity':
            return html`<agents-activity .store=${bind.store} .api=${bind.api}></agents-activity>`
          case 'connections':
            return html`<agents-connections .store=${bind.store} .api=${bind.api} .routeOwned=${true}></agents-connections>`
          case 'models':
            return html`<agents-models .store=${bind.store} .api=${bind.api} .routeOwned=${true}></agents-models>`
        }
    }
  }

  private renderCreateRoute(route: Extract<Route, { kind: 'create' }>): TemplateResult | DirectiveResult {
    const bind = { store: this.store, api: this.api }
    // A context rotation can leave an in-flight mutation on the old create
    // child. Keying the route-owned surface forces Lit to detach that child
    // when the session changes instead of overwriting its store/session
    // properties in place. Late events from the detached child cannot bubble;
    // events delivered before the replacement render still fail the marker
    // checks above.
    const render = (view: TemplateResult): TemplateResult | DirectiveResult => keyed(this.createSession, view)
    switch (route.resource) {
      case 'agent':
        return render(html`<agents-agent-create .createSession=${this.createSession} .store=${bind.store} .api=${bind.api}></agents-agent-create>`)
      case 'connection':
        return render(html`<agents-connections
          .createSession=${this.createSession}
          .store=${bind.store}
          .api=${bind.api}
          .routeOwned=${true}
          .createRoute=${true}
          .createType=${route.type || ''}
        ></agents-connections>`)
      case 'toolset':
        return render(html`<agents-toolsets
          .createSession=${this.createSession}
          .store=${bind.store}
          .api=${bind.api}
          .routeOwned=${true}
          .createRoute=${true}
        ></agents-toolsets>`)
      case 'model':
        return render(html`<agents-models .createSession=${this.createSession} .store=${bind.store} .api=${bind.api} .routeOwned=${true} .createRoute=${true}></agents-models>`)
    }
  }

  private createOwnerRoute(route: Extract<Route, { kind: 'create' }>): Route {
    if (route.resource === 'model') return { kind: 'menu', menu: 'models' }
    if (route.resource === 'connection' || route.resource === 'toolset') return { kind: 'menu', menu: 'connections' }
    return { kind: 'menu', menu: 'agents' }
  }

  private createSuccessRoute(detail: CreateSuccessDetail): Route {
    if (detail.resource === 'agent' && detail.name) return { kind: 'agent', name: detail.name, tab: 'config' }
    if (detail.resource === 'model') return { kind: 'menu', menu: 'models' }
    if (detail.resource === 'connection' || detail.resource === 'toolset') return { kind: 'menu', menu: 'connections' }
    return { kind: 'menu', menu: 'agents' }
  }

  private adoptCreateResult(detail: CreateSuccessDetail): void {
    if (!detail.item) return
    switch (detail.resource) {
      case 'agent': {
        const item = detail.item as Agent
        if (!item.metadata?.name) return
        this.store.adopt('agents', item)
        break
      }
      case 'connection': {
        const item = detail.item as Connection
        if (!item.metadata?.name) return
        this.store.adopt('connections', item)
        break
      }
      case 'toolset': {
        const item = detail.item as Toolset
        if (!item.metadata?.name) return
        this.store.adopt('toolsets', item)
        break
      }
      case 'model': {
        const item = detail.item as Credential
        if (!item.name) return
        this.store.adopt('credentials', item)
        break
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
    return html`<nav class="${tabsClass('agents-nav')}" aria-label="Agents provider sections">
      ${MENUS.map((m) => {
        const meta = MENU_META[m]
        const n = counts[m]
        const isActive = m === active
        return html`<button
          class="k-btn k-btn--ghost ${tabClass({ active: isActive })} agents-navtab ${isActive ? 'sel' : ''}"
          type="button"
          aria-current=${isActive ? 'page' : nothing}
          @click=${() => this.go({ kind: 'menu', menu: m })}
        >
          <span class="k-tab__icon">${icon(meta.icon)}</span>
          <span>${meta.label}</span>
          ${n
            ? html`<span class="${tabCountClass({ attention: m === 'activity' })} agents-navcount ${m === 'activity' ? 'attn' : ''}">${n}</span>`
            : nothing}
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
