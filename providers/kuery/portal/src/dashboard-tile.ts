// Dashboard tile for kuery, mounted by <railgrid-dashboard-tile-kuery>
// (see main.ts).
//
// The tile reports the two numbers that decide whether a query will answer
// anything: how many SavedViews the workspace has, and how many edges are
// connected for them to run over. An empty edge list is the single most common
// reason a query comes back with nothing, and a workspace with no saved view
// has nothing to run at all.
//
// Both come from the kube client rather than from kuery: SavedViews are
// kuery's own kind in the tenant's workspace and edges are the edges
// provider's, so the tile reads what the signed-in user is allowed to read and
// the provider keeps its single tenant route. Edge LIFECYCLE still belongs to
// the edges provider's tile; this one only counts.
//
// Plain DOM using portalkit's framework-neutral dashboard tile semantics.

import { ic } from './portalkit/icons'
import {
  TILE_ROWS,
  createTilePoller,
  dashboardTileSemanticClass,
  hasWorkspaceContext,
  isBenignTileError,
  tileErrorText,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'
import { createKueryRequestContext } from './request-context'
import { kubeClientFor, listEdges, listSavedViews, type SavedView } from './savedviews'

export class KueryDashboardTile extends HTMLElement {
  private _ctx: TileContext | null = null
  private _poller: TilePoller | null = null
  private _edges: string[] = []
  private _views: SavedView[] = []
  private _loading = true
  private _error: string | null = null
  private _contextGeneration = 0
  private _connected = false
  private _lastHTML = ''

  set railgridContext(v: TileContext | null) {
    const changed = createKueryRequestContext(v).identity !== createKueryRequestContext(this._ctx).identity
    this._ctx = v
    if (changed) {
      this._contextGeneration += 1
      this._edges = []
      this._views = []
      this._error = null
      this._loading = true
      if (this._connected) this._render()
    }
    this._poller?.refresh()
  }
  get railgridContext(): TileContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._connected = true
    this._render()
    if (!this._poller) {
      this._poller = createTilePoller(() => this._load())
      this._poller.start()
    }
  }

  disconnectedCallback(): void {
    this._connected = false
    this._contextGeneration += 1
    this._poller?.stop()
    this._poller = null
  }

  private async _load(): Promise<void> {
    const generation = this._contextGeneration
    const request = createKueryRequestContext(this._ctx)
    const isCurrent = (): boolean =>
      this._connected &&
      generation === this._contextGeneration &&
      createKueryRequestContext(this._ctx).identity === request.identity
    const ctx = this._ctx
    const kube = kubeClientFor(request)
    if (!hasWorkspaceContext(ctx) || !kube) {
      if (!isCurrent()) return
      this._edges = []
      this._views = []
      this._error = null
      this._loading = false
      this._render()
      return
    }
    try {
      const [edges, views] = await Promise.all([listEdges(kube), listSavedViews(kube)])
      if (!isCurrent()) return
      this._edges = edges
      this._views = views
      this._error = null
    } catch (e) {
      if (!isCurrent()) return
      this._edges = []
      this._views = []
      this._error = isBenignTileError(e) ? null : tileErrorText(e)
    } finally {
      if (!isCurrent()) return
      this._loading = false
      this._render()
    }
  }

  private _navigate(path: string): void {
    this.dispatchEvent(new CustomEvent('railgrid-navigate', { detail: { path }, bubbles: true }))
  }

  private _render(): void {
    if (this._loading) {
      this._commit(`<div class="${dashboardTileSemanticClass.message}" role="status" aria-live="polite" aria-atomic="true">Loading edges…</div>`)
      return
    }
    if (this._error) {
      this._commit(`<div class="${dashboardTileSemanticClass.error}" role="alert">Failed to load: ${escapeHTML(this._error)}</div>`)
      return
    }

    const names = this._views
      .map((view) => view.spec?.displayName || view.metadata.name)
      .filter((name): name is string => !!name)
    const rows = names.slice(0, TILE_ROWS)
    const more = names.length - rows.length
    const stats = `<span class="${dashboardTileSemanticClass.stat} ${dashboardTileSemanticClass.statTotal}">${ic('search', dashboardTileSemanticClass.statIcon)}<strong class="${dashboardTileSemanticClass.statNum}">${this._views.length}</strong> <span class="${dashboardTileSemanticClass.statLabel}">${
      this._views.length === 1 ? 'saved view' : 'saved views'
    }</span></span><span class="${dashboardTileSemanticClass.stat}">${ic('cpu', dashboardTileSemanticClass.statIcon)}<strong class="${dashboardTileSemanticClass.statNum}">${this._edges.length}</strong> <span class="${dashboardTileSemanticClass.statLabel}">${
      this._edges.length === 1 ? 'edge engaged' : 'edges engaged'
    }</span></span>`

    const body = rows.length
      ? `<div>
           <div class="${dashboardTileSemanticClass.sectionLabel}">Saved views</div>
           <ul class="${dashboardTileSemanticClass.list}">${rows
             .map(
               (name) => `<li><button type="button" class="${dashboardTileSemanticClass.row}" data-view="${escapeHTML(name)}">
                 <span class="${dashboardTileSemanticClass.rowDot} kuery-tile-dot--success"></span>
                 <span class="${dashboardTileSemanticClass.rowPrimary}">${escapeHTML(name)}</span>
                 ${chevron()}
               </button></li>`,
             )
             .join('')}</ul>
           ${more > 0 ? `<div class="${dashboardTileSemanticClass.rowSecondary}">+${more} more</div>` : ''}
         </div>`
      : this._edges.length
        ? `<p class="${dashboardTileSemanticClass.empty}">No saved views yet — open Kuery and run a query to make one.</p>`
        : `<p class="${dashboardTileSemanticClass.empty}">No edges to query yet — enroll one in Edges first.</p>`

    const liveText = `${this._views.length} saved ${this._views.length === 1 ? 'view' : 'views'} over ${this._edges.length} engaged ${this._edges.length === 1 ? 'edge' : 'edges'}.`
    const html = `<span class="kuery-tile-live" role="status" aria-live="polite" aria-atomic="true">${liveText}</span><div class="${dashboardTileSemanticClass.root}"><div class="${dashboardTileSemanticClass.stats}">${stats}</div>${body}</div>`
    if (!this._commit(html)) return

    for (const el of Array.from(this.querySelectorAll<HTMLButtonElement>('button[data-view]'))) {
      // The shell is the only destination this provider has; opening it is the
      // useful action.
      el.addEventListener('click', () => this._navigate(''))
    }
  }

  private _commit(html: string): boolean {
    if (html === this._lastHTML) return false
    this._lastHTML = html
    this.innerHTML = html
    return true
  }
}

function chevron(): string {
  return `<svg class="${dashboardTileSemanticClass.chevron}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>`
}

// Names come from the API and land in an HTML string, so escape them.
function escapeHTML(v: string): string {
  return v.replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string,
  )
}
