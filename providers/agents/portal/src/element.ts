// AgentsElement is the custom element the kedge portal renders for the agents
// provider. The portal loads main.js (registering this element), appends the
// element, sets element.kedgeContext as a JS property, and listens for bubbled
// events. The element runs in light DOM so the portal's CSS variables cascade.
//
// Information architecture: a persistent MENU bar of global entity lists
// (Agents · Connections · Toolsets · Schedules · Triggers · Models · Inbox).
// Objects are global and agent-agnostic. Clicking an agent opens its DETAIL page
// (Chat first, then Flow and Settings) — chat is always scoped to a known agent.
//
// This element is a thin shell: it owns the KedgeContext, one ApiClient, one
// AppStore, the current Route, and the render dispatch. Every view lives in
// views/*; mutations go through actions.ts.

import { ApiClient } from './api'
import { AppStore } from './store'
import type { KedgeContext } from './types'
import { ic, type IconName } from './portalkit/icons'
import { escapeHTML } from './types'
import { DEFAULT_ROUTE, MENUS, parseHash, syncHash, type MenuKey, type Route } from './router'
import type { ViewCtx } from './view'

import * as agentsView from './views/agents'
import * as agentDetail from './views/agent-detail'
import * as connectionsView from './views/connections'
import * as toolsetsView from './views/toolsets'
import * as schedulesView from './views/schedules'
import * as triggersView from './views/triggers'
import * as modelsView from './views/models'
import * as inboxView from './views/inbox'
import { resetForTenant as resetChat } from './views/agent-chat'
import { resetForTenant as resetModels } from './views/models'

const MENU_META: Record<MenuKey, { icon: IconName; label: string }> = {
  agents: { icon: 'bot', label: 'Agents' },
  connections: { icon: 'plug', label: 'Connections' },
  toolsets: { icon: 'package', label: 'Toolsets' },
  schedules: { icon: 'clock', label: 'Schedules' },
  triggers: { icon: 'zap', label: 'Triggers' },
  models: { icon: 'cpu', label: 'Models' },
  inbox: { icon: 'inbox', label: 'Inbox' },
}

export class AgentsElement extends HTMLElement {
  private _ctx: KedgeContext | null = null
  private _api = new ApiClient()
  private _store: AppStore
  private _route: Route = DEFAULT_ROUTE
  private _note: string | null = null
  private _loadedTenant: string | null = null

  constructor() {
    super()
    this._store = new AppStore(this._api, () => this._render())
  }

  set kedgeContext(v: KedgeContext | null) {
    this._ctx = v
    this._api.setContext(v)
    this._render()
    this._maybeLoad()
  }
  get kedgeContext(): KedgeContext | null {
    return this._ctx
  }

  private _onHashChange = (): void => {
    this._route = parseHash()
    this._render()
  }
  connectedCallback(): void {
    this._route = parseHash()
    window.addEventListener('hashchange', this._onHashChange)
    this._render()
    this._maybeLoad()
  }
  disconnectedCallback(): void {
    window.removeEventListener('hashchange', this._onHashChange)
  }

  private _ctxObj(): ViewCtx {
    return {
      store: this._store,
      api: this._api,
      route: this._route,
      navigate: (r) => this._navigate(r),
      notify: (m) => {
        this._note = m
        this._render()
      },
      rerender: () => this._render(),
    }
  }

  private _navigate(r: Route): void {
    this._route = r
    this._note = null
    syncHash(r)
    this._render()
  }

  private _maybeLoad(): void {
    if (!this._ctx?.basePath || !this._api.hasWorkspace()) return
    const key = this._api.tenantKey()
    if (key === this._loadedTenant) return
    // Switching tenants (not the first load) resets to the Agents menu so we
    // never show a stale agent from another workspace. On first load we keep the
    // hash-restored route so a refresh stays put.
    if (this._loadedTenant !== null) {
      resetChat()
      resetModels()
      this._route = DEFAULT_ROUTE
      syncHash(this._route)
    }
    this._loadedTenant = key
    resetChat()
    resetModels()
    this._store.loadAll()
  }

  // ---- rendering: shell ----------------------------------------------------

  private _render(): void {
    if (!this._ctx) {
      this.innerHTML = `<div class="agents-empty"><p class="muted">Connecting…</p></div>`
      return
    }
    if (!this._api.hasWorkspace()) {
      this.innerHTML = `<div class="agents-empty"><p class="muted">Select an organization and workspace in the sidebar to use your agents.</p></div>`
      return
    }
    syncHash(this._route)
    const vc = this._ctxObj()

    let inner: string
    if (this._route.kind === 'agent') {
      inner = agentDetail.render(vc, this._route.name, this._route.tab)
    } else {
      inner = `<div class="agents-page">${this._renderMenu(this._route.menu, vc)}</div>`
    }

    this.innerHTML = `
      <div class="agents-app">
        ${this._renderNav()}
        ${this._note ? `<div class="agents-note" data-clear-note>${escapeHTML(this._note)}</div>` : ''}
        ${this._store.error ? `<div class="agents-err">${escapeHTML(this._store.error)}</div>` : ''}
        ${inner}
      </div>`

    this.querySelector<HTMLElement>('[data-clear-note]')?.addEventListener('click', () => {
      this._note = null
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-nav]').forEach((el) =>
      el.addEventListener('click', () => this._navigate({ kind: 'menu', menu: el.dataset.nav as MenuKey })),
    )

    if (this._route.kind === 'agent') agentDetail.wire(vc, this, this._route.name, this._route.tab)
    else this._wireMenu(this._route.menu, vc)
  }

  private _renderNav(): string {
    const activeMenu = this._route.kind === 'agent' ? 'agents' : this._route.menu
    const pending = this._store.inbox.filter((i) => i.state === 'pending').length
    const count = (m: MenuKey): number => {
      switch (m) {
        case 'agents':
          return this._store.agents.length
        case 'connections':
          return this._store.connections.length
        case 'toolsets':
          return this._store.toolsets.length
        case 'schedules':
          return this._store.schedules.length
        case 'triggers':
          return this._store.triggers.length
        case 'models':
          return this._store.credentials.length
        case 'inbox':
          return pending
      }
    }
    const tabs = MENUS.map((m) => {
      const meta = MENU_META[m]
      const n = count(m)
      const badge = n ? ` <span class="agents-navcount">${n}</span>` : ''
      return `<button class="agents-navtab ${m === activeMenu ? 'sel' : ''}" data-nav="${m}">${ic(meta.icon)} ${meta.label}${badge}</button>`
    }).join('')
    return `<nav class="agents-nav">${tabs}</nav>`
  }

  private _renderMenu(menu: MenuKey, vc: ViewCtx): string {
    switch (menu) {
      case 'agents':
        return agentsView.render(vc)
      case 'connections':
        return connectionsView.render(vc)
      case 'toolsets':
        return toolsetsView.render(vc)
      case 'schedules':
        return schedulesView.render(vc)
      case 'triggers':
        return triggersView.render(vc)
      case 'models':
        return modelsView.render(vc)
      case 'inbox':
        return inboxView.render(vc)
    }
  }

  private _wireMenu(menu: MenuKey, vc: ViewCtx): void {
    switch (menu) {
      case 'agents':
        return agentsView.wire(vc, this)
      case 'connections':
        return connectionsView.wire(vc, this)
      case 'toolsets':
        return toolsetsView.wire(vc, this)
      case 'schedules':
        return schedulesView.wire(vc, this)
      case 'triggers':
        return triggersView.wire(vc, this)
      case 'models':
        return modelsView.wire(vc, this)
      case 'inbox':
        return inboxView.wire(vc, this)
    }
  }
}
