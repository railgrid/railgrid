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

  set railgridContext(v: TileContext | null) {
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
    this._poller ??= createTilePoller(() => this._load())
    this._poller.start()
    this._render()
  }

  disconnectedCallback(): void {
    this._poller?.stop()
  }

  private async _load(): Promise<void> {
    if (!hasWorkspaceContext(this._ctx)) {
      this._items = []
      this._error = ''
      this._render()
      return
    }
    try {
      const kube = createKubeClient({ fetch: providerFetch(this._ctx), cluster: this._ctx?.tenant as string })
      this._items = await kube.listAll<Greeting>(greetings)
      this._error = ''
    } catch (err) {
      this._items = []
      // A workspace that has not enabled this provider is empty, not broken.
      this._error = isBenignTileError(err) ? '' : tileErrorText(err)
    }
    this._render()
  }

  private _render(): void {
    const ready = this._items.filter(g => g.status?.conditions?.some(c => c.type === 'Ready' && c.status === 'True')).length
    const recent = mostRecent(this._items, g => g.status?.observedAt || g.metadata?.creationTimestamp)

    this.innerHTML = `
      <div class="${k.root}">
        <div class="${k.stats}">
          <span class="${k.stat} ${k.statTotal}"><span class="${k.statNum}">${this._items.length}</span> <span class="${k.statLabel}">greetings</span></span>
          <span class="${k.stat} ${k.statOk}"><span class="${k.statNum}">${ready}</span> <span class="${k.statLabel}">ready</span></span>
        </div>
        ${this._error ? `<p class="${k.error}">${escapeHTML(this._error)}</p>` : ''}
        ${
          recent.length === 0
            ? `<p class="${k.empty}">No greetings yet</p>`
            : `<ul class="${k.list}">${recent.map(g => this._row(g)).join('')}</ul>`
        }
      </div>
    `
    for (const row of this.querySelectorAll<HTMLButtonElement>('[data-open]')) {
      row.addEventListener('click', () => navigateFromTile(this, `greetings/${row.dataset.open}`))
    }
  }

  private _row(g: Greeting): string {
    const name = g.metadata?.name || ''
    const isReady = !!g.status?.conditions?.some(c => c.type === 'Ready' && c.status === 'True')
    return `
      <li>
        <button class="${k.row}" type="button" data-open="${escapeHTML(name)}">
          <span class="${k.rowDot} ${isReady ? k.statOk : k.statMuted}"></span>
          <span class="${k.rowPrimary}">${escapeHTML(name)}</span>
          <span class="${k.rowSecondary}">${g.status?.observedAt ? 'observed' : 'pending'}</span>
        </button>
      </li>
    `
  }
}
