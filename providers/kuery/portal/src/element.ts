// KueryElement — the kuery provider's portal UI: a fleet-wide object
// inventory over every edge connected to the workspace, with an impact
// drill-down (declared blast radius of one object). Backed by the
// provider's tenant-scoped /api/query + /api/edges (proxied by the hub
// at /services/providers/kuery/*, which injects X-Kedge-Tenant).
//
// Plain custom element in light DOM (the portal's CSS variables cascade
// in); see main.ts for registration and style.css for the rules.

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  // Sidebar-selected org/workspace UUIDs. Forwarded as X-Kedge-Org /
  // X-Kedge-Workspace so the hub backend proxy can resolve and inject
  // X-Kedge-Tenant; without them the resolver falls back to the user's
  // personal org (often unset in dev) and the backend 401s.
  orgUUID?: string | null
  workspaceUUID?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

// Minimal mirror of kuery's ObjectResult — only what the UI renders.
interface ObjectResult {
  id?: string
  cluster?: string
  object?: {
    kind?: string
    apiVersion?: string
    metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  }
  relations?: Record<string, ObjectResult[]>
}

interface QueryStatus {
  objects?: ObjectResult[]
  incomplete?: boolean
  warnings?: string[]
}

// The relation set the impact view expands — keep in lockstep with
// mcpserver/tools.go impactRelations.
const IMPACT_RELATIONS = ['descendants+', 'references', 'selects', 'selected-by', 'owners', 'linked+', 'grouped']

const RELATION_TITLES: Record<string, string> = {
  'descendants+': 'Descendants (transitive)',
  references: 'References (spec fields)',
  selects: 'Selects (label selectors)',
  'selected-by': 'Selected by',
  owners: 'Owners',
  'linked+': 'Linked (cross-edge, transitive)',
  grouped: 'Grouped (cross-edge)',
}

export class KueryElement extends HTMLElement {
  private _ctx: KedgeContext | null = null
  private _booted = false

  private _edges: string[] = []
  private _rows: ObjectResult[] = []
  private _incomplete = false
  private _queryError = ''
  private _loading = false

  // Impact drill-down state. null = inventory view.
  private _impactOf: ObjectResult | null = null
  private _impact: ObjectResult | null = null
  private _impactError = ''

  // Filter state survives re-renders.
  private _fEdge = ''
  private _fKind = ''
  private _fNamespace = ''
  private _fName = ''

  set kedgeContext(v: KedgeContext | null) {
    this._ctx = v
    this._render()
    this._boot()
  }
  get kedgeContext(): KedgeContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._render()
    this._boot()
  }

  // _boot waits for basePath (it can arrive on a later context push), then
  // loads the edge list and the initial unfiltered inventory.
  private _boot(): void {
    if (this._booted || !this._ctx?.basePath) return
    this._booted = true
    void this._loadEdges()
    void this._runQuery()
  }

  private _apiBase(): string {
    return (this._ctx?.basePath || '').replace(/^\/ui\/providers\//, '/services/providers/')
  }

  // _tenantHeaders carries the identity the hub backend proxy needs to
  // inject X-Kedge-Tenant: the bearer token (to identify the user) plus
  // the sidebar-selected org/workspace (to pick the tenant). Mirrors what
  // the console's own /api/orgs/* requests send.
  private _tenantHeaders(): Record<string, string> {
    const h: Record<string, string> = {}
    if (this._ctx?.token) h['Authorization'] = `Bearer ${this._ctx.token}`
    if (this._ctx?.orgUUID) h['X-Kedge-Org'] = this._ctx.orgUUID
    if (this._ctx?.workspaceUUID) h['X-Kedge-Workspace'] = this._ctx.workspaceUUID
    return h
  }

  private async _fetchJSON(path: string, init?: RequestInit): Promise<unknown> {
    const res = await fetch(this._apiBase() + path, {
      credentials: 'same-origin',
      ...init,
      headers: { ...this._tenantHeaders(), ...(init?.headers as Record<string, string> | undefined) },
    })
    const body = await res.text()
    if (!res.ok) throw new Error(`${res.status} ${body.slice(0, 300)}`)
    return body ? JSON.parse(body) : {}
  }

  private async _loadEdges(): Promise<void> {
    try {
      const out = (await this._fetchJSON('/api/edges')) as { edges?: string[] }
      this._edges = out.edges ?? []
    } catch {
      this._edges = []
    }
    this._render()
  }

  private async _runQuery(): Promise<void> {
    this._loading = true
    this._queryError = ''
    this._render()

    const filter: Record<string, unknown> = {}
    if (this._fKind) filter.groupKind = { kind: this._fKind }
    if (this._fNamespace) filter.namespace = this._fNamespace
    if (this._fName) filter.name = this._fName

    const spec: Record<string, unknown> = {
      limit: 100,
      objects: { id: true, cluster: true },
    }
    if (this._fEdge) spec.cluster = { name: this._fEdge }
    if (Object.keys(filter).length > 0) spec.filter = { objects: [filter] }

    try {
      const status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      })) as QueryStatus
      this._rows = status.objects ?? []
      this._incomplete = !!status.incomplete
    } catch (e) {
      this._rows = []
      this._queryError = e instanceof Error ? e.message : String(e)
    }
    this._loading = false
    this._render()
  }

  private async _runImpact(row: ObjectResult): Promise<void> {
    this._impactOf = row
    this._impact = null
    this._impactError = ''
    this._render()

    const relations: Record<string, unknown> = {}
    for (const rel of IMPACT_RELATIONS) relations[rel] = {}

    const o = row.object ?? {}
    const meta = o.metadata ?? {}
    const group = (o.apiVersion || '').includes('/') ? (o.apiVersion as string).split('/')[0] : ''
    const filter: Record<string, unknown> = { name: meta.name }
    if (o.kind) filter.groupKind = { apiGroup: group, kind: o.kind }
    if (meta.namespace) filter.namespace = meta.namespace

    const spec: Record<string, unknown> = {
      maxDepth: 5,
      filter: { objects: [filter] },
      objects: { id: true, cluster: true, relations },
    }
    // row.cluster is "{tenant}/{edge}" — the API re-pins the prefix, so
    // passing it back verbatim is fine and keeps the anchor unambiguous.
    if (row.cluster) spec.cluster = { name: row.cluster }

    try {
      const status = (await this._fetchJSON('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      })) as QueryStatus
      this._impact = status.objects?.[0] ?? null
      if (!this._impact) this._impactError = 'object not found (sync may be catching up)'
    } catch (e) {
      this._impactError = e instanceof Error ? e.message : String(e)
    }
    this._render()
  }

  // ── rendering ────────────────────────────────────────────────────────

  private _render(): void {
    if (this._impactOf) {
      this.innerHTML = this._renderImpact()
      this.querySelector('#impact-back')?.addEventListener('click', () => {
        this._impactOf = null
        this._impact = null
        this._render()
      })
      return
    }
    this.innerHTML = this._renderInventory()
    this._bindInventory()
  }

  private _renderInventory(): string {
    const edgeOptions = ['<option value="">all edges</option>']
      .concat(this._edges.map((e) => `<option value="${esc(e)}"${e === this._fEdge ? ' selected' : ''}>${esc(e)}</option>`))
      .join('')

    let body: string
    if (this._loading) {
      body = `<tr><td colspan="5" class="muted">querying the fleet…</td></tr>`
    } else if (this._queryError) {
      body = `<tr><td colspan="5" class="error">${esc(this._queryError)}</td></tr>`
    } else if (this._rows.length === 0) {
      body = `<tr><td colspan="5" class="muted">no objects match — connect an edge or relax the filters</td></tr>`
    } else {
      body = this._rows.map((r, i) => {
        const o = r.object ?? {}
        const m = o.metadata ?? {}
        return `<tr data-row="${i}" class="row">
          <td><code>${esc(edgeOf(r.cluster))}</code></td>
          <td>${esc(o.kind || '?')}</td>
          <td>${esc(m.namespace || '—')}</td>
          <td class="name">${esc(m.name || '?')}</td>
          <td class="muted">${esc(age(m.creationTimestamp))}</td>
        </tr>`
      }).join('')
    }

    return `
      <div class="panel">
        <div class="panel-head">
          <h2 class="panel-title">Fleet inventory</h2>
          <span class="badge ${this._edges.length ? 'ok' : 'warn'}">${this._edges.length} edge${this._edges.length === 1 ? '' : 's'} engaged</span>
        </div>
        <p class="meta">One query across every connected edge. Click a row for its impact (declared blast radius).</p>
        <div class="toolbar">
          <select id="f-edge">${edgeOptions}</select>
          <input id="f-kind" placeholder="kind (e.g. Deployment)" value="${esc(this._fKind)}" />
          <input id="f-ns" placeholder="namespace" value="${esc(this._fNamespace)}" />
          <input id="f-name" placeholder="name (exact)" value="${esc(this._fName)}" />
          <button id="f-go">Search</button>
        </div>
        <table class="objects">
          <thead><tr><th>edge</th><th>kind</th><th>namespace</th><th>name</th><th>age</th></tr></thead>
          <tbody>${body}</tbody>
        </table>
        ${this._incomplete ? '<p class="meta">result truncated — narrow the filters</p>' : ''}
      </div>
    `
  }

  private _bindInventory(): void {
    const read = () => {
      this._fEdge = (this.querySelector('#f-edge') as HTMLSelectElement | null)?.value ?? ''
      this._fKind = (this.querySelector('#f-kind') as HTMLInputElement | null)?.value.trim() ?? ''
      this._fNamespace = (this.querySelector('#f-ns') as HTMLInputElement | null)?.value.trim() ?? ''
      this._fName = (this.querySelector('#f-name') as HTMLInputElement | null)?.value.trim() ?? ''
    }
    this.querySelector('#f-go')?.addEventListener('click', () => {
      read()
      void this._runQuery()
    })
    this.querySelectorAll('input').forEach((el) =>
      el.addEventListener('keydown', (ev) => {
        if ((ev as KeyboardEvent).key === 'Enter') {
          read()
          void this._runQuery()
        }
      }),
    )
    this.querySelectorAll('tr.row').forEach((tr) =>
      tr.addEventListener('click', () => {
        const i = Number((tr as HTMLElement).dataset.row)
        const row = this._rows[i]
        if (row) void this._runImpact(row)
      }),
    )
  }

  private _renderImpact(): string {
    const o = this._impactOf?.object ?? {}
    const m = o.metadata ?? {}
    const title = `${o.kind || '?'} ${m.namespace ? m.namespace + '/' : ''}${m.name || '?'}`

    let body: string
    if (this._impactError) {
      body = `<p class="error">${esc(this._impactError)}</p>`
    } else if (!this._impact) {
      body = `<p class="muted">expanding declared coupling…</p>`
    } else {
      const rels = this._impact.relations ?? {}
      const sections = IMPACT_RELATIONS.filter((r) => (rels[r] ?? []).length > 0).map((r) => {
        const items = (rels[r] ?? []).map((rr) => {
          const ro = rr.object ?? {}
          const rm = ro.metadata ?? {}
          return `<li><code>${esc(edgeOf(rr.cluster))}</code> ${esc(ro.kind || '?')} <span class="name">${esc(rm.namespace ? rm.namespace + '/' : '')}${esc(rm.name || '?')}</span></li>`
        }).join('')
        return `<h3 class="rel-title">${esc(RELATION_TITLES[r] ?? r)} <span class="muted">(${(rels[r] ?? []).length})</span></h3><ul class="rel-list">${items}</ul>`
      })
      body = sections.length > 0
        ? sections.join('')
        : '<p class="muted">no declared coupling found — nothing references, selects, or descends from this object (network-level dependencies are not visible to kuery)</p>'
    }

    return `
      <div class="panel">
        <div class="panel-head">
          <h2 class="panel-title">Impact: ${esc(title)}</h2>
          <button id="impact-back">← inventory</button>
        </div>
        <p class="meta">Declared blast radius on <code>${esc(edgeOf(this._impactOf?.cluster))}</code> — owners, descendants, spec references, selector matches, and cross-edge links. Not a network dependency map.</p>
        ${body}
      </div>
    `
  }
}

// ── helpers ───────────────────────────────────────────────────────────

// edgeOf strips the "{tenant}/" prefix from an engaged cluster key.
function edgeOf(cluster?: string): string {
  if (!cluster) return '?'
  const i = cluster.lastIndexOf('/')
  return i === -1 ? cluster : cluster.slice(i + 1)
}

function age(ts?: string): string {
  if (!ts) return ''
  const ms = Date.now() - new Date(ts).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const mins = Math.floor(ms / 60000)
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  if (hours < 48) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
