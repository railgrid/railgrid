// Dashboard tile for kuery, mounted by <faros-dashboard-tile-kuery>
// (see main.ts).
//
// kuery owns no resources of its own — it is a query surface over the edges
// another provider enrolls. So this tile deliberately reports the ONE thing it
// can state truthfully from its own API: how many edges are currently
// queryable, and which. That is genuinely useful (an empty list is why a query
// returns nothing) and it is honest about scope: the edge lifecycle belongs to
// the edges provider's tile, not this one.
//
// Plain DOM, mirroring portalkit/dashboardtile's tileClass — this portal ships
// no renderer and no Tailwind build.

import { ic } from './portalkit/icons'
import {
  TILE_ROWS,
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  tileErrorText,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'

export class KueryDashboardTile extends HTMLElement {
  private _ctx: TileContext & { orgUUID?: string | null; workspaceUUID?: string | null } | null = null
  private _poller: TilePoller | null = null
  private _edges: string[] = []
  private _loading = true
  private _error: string | null = null

  set farosContext(v: (TileContext & { orgUUID?: string | null; workspaceUUID?: string | null }) | null) {
    this._ctx = v
    this._poller?.refresh()
  }
  get farosContext(): TileContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    if (!this._poller) {
      this._poller = createTilePoller(() => this._load())
      this._poller.start()
    }
  }

  disconnectedCallback(): void {
    this._poller?.stop()
    this._poller = null
  }

  private async _load(): Promise<void> {
    if (!hasWorkspaceContext(this._ctx)) {
      this._edges = []
      this._error = null
      this._loading = false
      this._render()
      return
    }
    const base = (this._ctx?.basePath || '').replace(/^\/ui\/providers\//, '/services/providers/')
    const headers: Record<string, string> = {}
    if (this._ctx?.token) headers['Authorization'] = `Bearer ${this._ctx.token}`
    if (this._ctx?.orgUUID) headers['X-Faros-Org'] = this._ctx.orgUUID
    if (this._ctx?.workspaceUUID) headers['X-Faros-Workspace'] = this._ctx.workspaceUUID
    try {
      const res = await fetch(base + '/api/edges', { credentials: 'same-origin', headers })
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
      const out = (await res.json()) as { edges?: string[] }
      this._edges = out.edges ?? []
      this._error = null
    } catch (e) {
      this._edges = []
      this._error = isBenignTileError(e) ? null : tileErrorText(e)
    } finally {
      this._loading = false
      this._render()
    }
  }

  private _navigate(path: string): void {
    this.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
  }

  private _render(): void {
    if (this._loading) {
      this.innerHTML = '<div class="kuery-tile-msg">Loading edges…</div>'
      return
    }
    if (this._error) {
      this.innerHTML = `<div class="kuery-tile-err">Failed to load: ${escapeHTML(this._error)}</div>`
      return
    }

    const rows = this._edges.slice(0, TILE_ROWS)
    const more = this._edges.length - rows.length
    const stats = `<span class="kuery-tile-stat">${ic('search')}<strong>${this._edges.length}</strong> ${
      this._edges.length === 1 ? 'edge queryable' : 'edges queryable'
    }</span>`

    const body = rows.length
      ? `<div>
           <div class="kuery-tile-label">Edges</div>
           <ul class="kuery-tile-rows">${rows
             .map(
               (name) => `<li><button type="button" data-edge="${escapeHTML(name)}">
                 <span class="kuery-tile-dot"></span>
                 <span class="kuery-tile-name">${escapeHTML(name)}</span>
                 ${chevron()}
               </button></li>`,
             )
             .join('')}</ul>
           ${more > 0 ? `<div class="kuery-tile-more">+${more} more</div>` : ''}
         </div>`
      : `<p class="kuery-tile-empty">No edges to query yet — enroll one in Edges first.</p>`

    this.innerHTML = `<div class="kuery-tile"><div class="kuery-tile-stats">${stats}</div>${body}</div>`

    for (const el of Array.from(this.querySelectorAll<HTMLButtonElement>('button[data-edge]'))) {
      // The playground is the only destination this provider has; opening it
      // for the clicked edge is the useful action.
      el.addEventListener('click', () => this._navigate(''))
    }
  }
}

function chevron(): string {
  return `<svg class="kuery-tile-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>`
}

// Edge names come from the API and land in an HTML string, so escape them.
function escapeHTML(v: string): string {
  return v.replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string,
  )
}
