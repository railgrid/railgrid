// Dashboard tile for vibe-studio, mounted by
// <faros-dashboard-tile-vibe-studio> (see main.ts).
//
// Projects are the durable thing ("your apps"); sessions are the drafts you
// left open. The tile leads with the project count and calls out sessions
// still in flight, because a forgotten running session is the state a user
// most often wants to come back to — and the one nothing else surfaces.
//
// Plain DOM rather than a framework: this portal ships no renderer, and a
// four-row card does not justify pulling one in. The layout mirrors
// portalkit/dashboardtile's tileClass so the card matches every other tile.

import { serviceBase, tenantHeaders, hasWorkspace } from './portalkit/tenant'
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

interface ProjectRecord {
  name: string
  displayName: string
  phase?: string
  updatedAt?: string
}

interface SessionRecord {
  id: string
  phase: string
  updatedAt: string
}

// Phases that mean the session is still doing something. Everything else is
// finished, failed, or abandoned — none of which needs the user back.
const LIVE_PHASES = new Set(['Running', 'Starting', 'Active', 'Pending'])

export class VibeStudioDashboardTile extends HTMLElement {
  private _ctx: TileContext | null = null
  private _poller: TilePoller | null = null
  private _projects: ProjectRecord[] = []
  private _sessions: SessionRecord[] = []
  private _loading = true
  private _error: string | null = null

  set farosContext(v: TileContext | null) {
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

  private _apiBase(): string {
    return serviceBase(this._ctx?.basePath || '/ui/providers/vibe-studio') + '/api'
  }

  private async _get<T>(path: string): Promise<T> {
    const res = await fetch(this._apiBase() + path, {
      credentials: 'same-origin',
      headers: tenantHeaders({ token: this._ctx?.token }),
    })
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
    return (await res.json()) as T
  }

  private async _load(): Promise<void> {
    // hasWorkspace() is the same guard the provider page uses: without an
    // org/workspace the API answers for the wrong scope.
    if (!hasWorkspaceContext(this._ctx) || !hasWorkspace()) {
      this._projects = []
      this._sessions = []
      this._error = null
      this._loading = false
      this._render()
      return
    }
    try {
      const [projects, sessions] = await Promise.all([
        this._get<{ items?: ProjectRecord[] }>('/projects'),
        this._get<{ items?: SessionRecord[] }>('/sessions'),
      ])
      this._projects = projects.items ?? []
      this._sessions = sessions.items ?? []
      this._error = null
    } catch (e) {
      this._projects = []
      this._sessions = []
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
      this.innerHTML = '<div class="vibe-tile-msg">Loading projects…</div>'
      return
    }
    if (this._error) {
      this.innerHTML = `<div class="vibe-tile-err">Failed to load: ${escapeHTML(this._error)}</div>`
      return
    }

    const live = this._sessions.filter((s) => LIVE_PHASES.has(s.phase)).length
    const rows = [...this._projects]
      .sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || ''))
      .slice(0, TILE_ROWS)

    const stats = [
      `<span class="vibe-tile-stat">${ic('package')}<strong>${this._projects.length}</strong> ${
        this._projects.length === 1 ? 'project' : 'projects'
      }</span>`,
      live > 0
        ? `<span class="vibe-tile-stat vibe-tile-ok">${ic('play')}<strong>${live}</strong> live</span>`
        : '',
    ].join('')

    const body = rows.length
      ? `<div>
           <div class="vibe-tile-label">Recent</div>
           <ul class="vibe-tile-rows">${rows
             .map(
               (p) => `<li><button type="button" data-project="${escapeAttr(p.name)}">
                 <span class="vibe-tile-dot ${p.phase === 'Ready' ? 'vibe-tile-dot-ok' : 'vibe-tile-dot-idle'}"></span>
                 <span class="vibe-tile-name">${escapeHTML(p.displayName || p.name)}</span>
                 <span class="vibe-tile-sec">${escapeHTML((p.phase || '').toLowerCase())}</span>
                 ${chevron()}
               </button></li>`,
             )
             .join('')}</ul>
         </div>`
      : `<p class="vibe-tile-empty">No projects yet — start one to get going.</p>`

    this.innerHTML = `<div class="vibe-tile"><div class="vibe-tile-stats">${stats}</div>${body}</div>`

    for (const el of Array.from(this.querySelectorAll<HTMLButtonElement>('button[data-project]'))) {
      el.addEventListener('click', () => this._navigate(el.dataset.project || ''))
    }
  }
}

function chevron(): string {
  return `<svg class="vibe-tile-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>`
}

// The tile builds HTML strings, so every interpolated value is escaped. Names
// come from the API, but a project display name is user-authored text.
function escapeHTML(v: string): string {
  return v.replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string,
  )
}
function escapeAttr(v: string): string {
  return escapeHTML(v)
}
