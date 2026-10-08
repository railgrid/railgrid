import {
  createTilePoller,
  dashboardTileSemanticClass as k,
  hasWorkspaceContext,
  isBenignTileError,
  mostRecent,
  navigateFromTile,
  tileErrorText,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'
import { createKubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import { providerFetch } from './portalkit/tenant'
import { escapeHTML } from './element'

// <railgrid-dashboard-tile-quickstart> is the optional second element a
// provider may register: a glanceable card the console mounts on its dashboard.
// It reads the same bound CRs the full surface does, through the same host
// transport — a tile is not a second data path.
//
// The plumbing (poll cadence, overlap guard, "no workspace yet" as empty rather
// than as an error, the navigate dispatch) comes from portalkit/dashboardtile;
// what stays here is what a Greeting is and how to say it.

const greetings: KubeResourceRef = {
  group: 'quickstart.providers.railgrid.ai',
  version: 'v1alpha1',
  resource: 'greetings',
}

interface Greeting extends KubeObject {
  status?: { observedAt?: string; conditions?: Array<{ type?: string; status?: string }> }
}

export class QuickstartDashboardTileElement extends HTMLElement {
  private _ctx: TileContext | null = null
  private _items: Greeting[] = []
  private _error = ''
  private _poller: TilePoller | null = null
  private _loaded = false
  private _loading = false
  private _connected = false
  private _generation = 0
  private _identity = ''

  set railgridContext(v: TileContext | null) {
    const hostTransport = typeof v?.fetch === 'function'
    const identity = `${v?.tenant ?? ''}|${v?.user?.userId ?? v?.user?.sub ?? v?.user?.email ?? ''}|${hostTransport}|${hostTransport ? '' : v?.token ?? ''}`
    if (identity !== this._identity) {
      this._identity = identity
      this._generation += 1
      this._items = []
      this._loaded = false
      this._error = ''
    }
    this._ctx = v
    // Coalesced by the poller: a refresh arriving mid-load is queued, not
    // dropped, which is the sequence every tile actually starts with.
    this._poller?.refresh()
    this._render()
  }
  get railgridContext(): TileContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._connected = true
    this._poller ??= createTilePoller(() => this._load())
    this._poller.start()
    this._render()
  }

  disconnectedCallback(): void {
    this._connected = false
    this._generation += 1
    this._poller?.stop()
  }

  private async _load(): Promise<void> {
    const ctx = this._ctx
    const generation = this._generation
    const isCurrent = () => this._connected && generation === this._generation
    if (!hasWorkspaceContext(ctx)) {
      this._items = []
      this._error = ''
      this._loaded = true
      this._loading = false
      this._render()
      return
    }
    this._loading = true
    if (!this._loaded) this._render()
    try {
      const kube = createKubeClient({ fetch: providerFetch(ctx), cluster: ctx?.tenant as string })
      const items = await kube.listAll<Greeting>(greetings)
      if (!isCurrent()) return
      this._items = items
      this._error = ''
      this._loaded = true
    } catch (err) {
      if (!isCurrent()) return
      if (isBenignTileError(err)) {
        // A workspace that has not enabled this provider is empty, not broken.
        this._items = []
        this._error = ''
        this._loaded = true
      } else this._error = tileErrorText(err)
    } finally {
      if (isCurrent()) {
        this._loading = false
        this._render()
      }
    }
  }

  private _render(): void {
    if (!this._loaded) {
      this.innerHTML = this._error
        ? `<div class="${k.error}" role="alert">Could not load greetings. ${escapeHTML(this._error)} <button class="k-dashboard-action" type="button" data-retry>Retry</button></div>`
        : `<div class="${k.message}" role="status">Loading greetings…</div>`
      this._bindRetry()
      return
    }
    const ready = this._items.filter(g => g.status?.conditions?.some(c => c.type === 'Ready' && c.status === 'True')).length
    const recent = mostRecent(this._items, g => g.status?.observedAt || g.metadata?.creationTimestamp)

    this.innerHTML = `
      <div class="${k.root}" aria-busy="${this._loading}">
        <div class="${k.stats}">
          <span class="${k.stat} ${k.statTotal}"><span class="${k.statNum}">${this._items.length}</span> <span class="${k.statLabel}">greetings</span></span>
          <span class="${k.stat} ${k.statOk}"><span class="${k.statNum}">${ready}</span> <span class="${k.statLabel}">ready</span></span>
        </div>
        ${this._error ? `<p class="${k.error}" role="alert">Showing the last successful result. ${escapeHTML(this._error)} <button class="k-dashboard-action" type="button" data-retry>Retry</button></p>` : ''}
        ${
          recent.length === 0
            ? `<p class="${k.empty}">No greetings yet</p>`
            : `<ul class="${k.list}">${recent.map(g => this._row(g)).join('')}</ul>`
        }
      </div>
    `
    this._bindRetry()
    for (const row of this.querySelectorAll<HTMLButtonElement>('[data-open]')) {
      row.addEventListener('click', () => navigateFromTile(this, `greetings/${row.dataset.open}`))
    }
  }

  private _bindRetry(): void {
    this.querySelector<HTMLButtonElement>('[data-retry]')?.addEventListener('click', () => this._poller?.refresh())
  }

  private _row(g: Greeting): string {
    const name = g.metadata?.name || ''
    const isReady = !!g.status?.conditions?.some(c => c.type === 'Ready' && c.status === 'True')
    return `
      <li>
        <button class="${k.row}" type="button" data-open="${escapeHTML(name)}">
          <span class="${k.rowDot} ${isReady ? k.statOk : k.statWarn}" aria-hidden="true"></span>
          <span class="${k.rowPrimary}">${escapeHTML(name)}</span>
          <span class="${k.rowSecondary}">${g.status?.observedAt ? 'observed' : 'pending'}</span>
        </button>
      </li>
    `
  }
}
