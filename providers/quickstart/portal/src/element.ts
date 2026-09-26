import { createKubeClient, type KubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import { ic } from './portalkit/icons'
import { providerFetch, type ProviderFetch } from './portalkit/tenant'

// QuickstartElement is the custom element the railgrid portal renders for this
// provider — Pillar 3 of the provider contract, in the smallest form that is
// still real.
//
// The host portal:
//   1. Loads main.js as one classic <script> tag; this module's side effect
//      registers the element with customElements.define.
//   2. Appends <railgrid-provider-quickstart> to its DOM.
//   3. Sets element.railgridContext as a JS PROPERTY (never an attribute), and
//      re-sets it on every change — theme, workspace switch, token rotation.
//      The setter below is therefore the element's only lifecycle hook that
//      matters: react to it, never poll for it.
//   4. Listens for railgrid-navigate CustomEvents bubbling out of the element.
//
// The element renders into light DOM so the portal's stylesheet and CSS
// variables cascade in (see style.css).

export interface RailgridContext {
  // fetch is the host-owned transport: it injects Authorization and the tenant
  // headers and refuses paths outside this provider's allow list. Every hub
  // request goes through portalkit providerFetch(ctx) — never the global fetch.
  fetch?: ProviderFetch | null
  /** @deprecated Read-only fallback for older hosts; use fetch. */
  token?: string | null
  user?: { email?: string; sub?: string } | null
  // tenant is the kcp logical-cluster ID of the active workspace. It is what
  // addresses kcp (/clusters/{tenant}) — the bound CRs and the data-plane
  // verb alike, because a verb is a kcp custom subresource on the same path.
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  // basePath is this provider's UI mount, /ui/providers/quickstart. This
  // element does not need it: the hub's backend proxy under it is only for a
  // provider's /oauth and /mcp routes, and the quickstart serves neither.
  basePath?: string
}

// greetings is the bound CR this portal reads and writes. Cluster-scoped, so
// no namespace anywhere.
const greetings: KubeResourceRef = {
  group: 'quickstart.providers.railgrid.ai',
  version: 'v1alpha1',
  resource: 'greetings',
}

interface Greeting extends KubeObject {
  spec?: { message?: string }
  status?: {
    observedAt?: string
    conditions?: Array<{ type?: string; status?: string; reason?: string; message?: string }>
  }
}

export class QuickstartElement extends HTMLElement {
  private _ctx: RailgridContext | null = null
  private _items: Greeting[] = []
  private _loaded = false
  private _error = ''
  private _busy = ''
  private _lastKey = ''
  // The last greeting the verb returned, shown once under the form.
  private _greeted = ''

  // The host sets this after appending and again on every change. Re-render,
  // and reload when the identity of the data actually changed.
  set railgridContext(v: RailgridContext | null) {
    this._ctx = v
    const key = `${v?.tenant ?? ''}|${typeof v?.fetch === 'function'}`
    const changed = key !== this._lastKey
    this._lastKey = key
    this._render()
    if (changed && this._canLoad()) void this._load()
  }
  get railgridContext(): RailgridContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._render()
    if (this._canLoad()) void this._load()
  }

  // A workspace and a transport are the two preconditions for any request. The
  // host may push a partial context first, so this is a guard, not a wait:
  // the setter calls back when the real one lands.
  private _canLoad(): boolean {
    return !!this._ctx?.tenant
  }

  private _kube(): KubeClient {
    return createKubeClient({
      fetch: providerFetch(this._ctx),
      cluster: this._ctx?.tenant as string,
      fieldManager: 'railgrid-provider-quickstart',
    })
  }

  private async _load(): Promise<void> {
    try {
      const list = await this._kube().list<Greeting>(greetings)
      this._items = list.items
      this._error = ''
    } catch (err) {
      this._items = []
      this._error = (err as Error).message
    }
    this._loaded = true
    this._render()
  }

  private async _create(name: string, message: string): Promise<void> {
    this._busy = 'create'
    this._render()
    try {
      await this._kube().create<Greeting>(greetings, {
        apiVersion: `${greetings.group}/${greetings.version}`,
        kind: 'Greeting',
        metadata: { name },
        spec: { message },
      })
      this._error = ''
      await this._load()
    } catch (err) {
      this._error = (err as Error).message
    } finally {
      this._busy = ''
      this._render()
    }
  }

  // The data-plane verb. It goes to the same place the list and the create
  // above do — kcp, at /clusters/{tenant} — because a verb is a kcp custom
  // subresource on the Greeting: greetings/{name}/greet. kcp authorizes the
  // caller with ordinary RBAC and forwards the request to the provider. There
  // is no hub-proxied spelling of a verb; the provider's backend URL is never
  // addressed from here.
  private async _greet(name: string): Promise<void> {
    const ctx = this._ctx
    if (!ctx?.tenant) return
    this._busy = `greet:${name}`
    this._render()
    try {
      const url = this._kube().verbPath(greetings, name, 'greet')
      const res = await providerFetch(ctx)(url, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ input: {} }),
      })
      const envelope = (await res.json()) as { result?: { greeting?: string }; error?: { message?: string } }
      this._error = res.ok ? '' : envelope.error?.message || `greet failed with HTTP ${res.status}`
      if (res.ok) this._greeted = envelope.result?.greeting || ''
    } catch (err) {
      this._error = (err as Error).message
    } finally {
      this._busy = ''
      this._render()
    }
  }

  // Navigation is a CustomEvent, never a router import and never a full-page
  // href: the host turns it into a push inside its own SPA.
  private _openDetail(name: string): void {
    this.dispatchEvent(
      new CustomEvent('railgrid-navigate', { detail: { path: `greetings/${name}` }, bubbles: true }),
    )
  }

  private _render(): void {
    const ctx = this._ctx
    this.innerHTML = `
      <div class="quickstart-grid">
        <section class="k-card quickstart-panel">
          <div class="quickstart-panel-head">
            <h2 class="quickstart-panel-title">New greeting</h2>
            <span class="k-badge ${ctx?.tenant ? 'k-badge--success' : 'k-badge--warning'}" role="status">
              ${ctx?.tenant ? 'Workspace ready' : 'No workspace'}
            </span>
          </div>
          <p class="quickstart-meta">Written straight to kcp as a Greeting CR with the portalkit kube client.</p>
          <form class="quickstart-form" data-form="create">
            <label class="quickstart-field">
              <span class="quickstart-label">Name</span>
              <input class="k-input" name="name" required pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?" placeholder="hello" />
            </label>
            <label class="quickstart-field">
              <span class="quickstart-label">Message</span>
              <input class="k-input" name="message" required maxlength="256" placeholder="Hello there" />
            </label>
            <button class="k-btn k-btn--primary" type="submit" ${ctx?.tenant ? '' : 'disabled'}>
              ${ic('plus')} ${this._busy === 'create' ? 'Creating…' : 'Create'}
            </button>
          </form>
          ${this._error ? `<p class="quickstart-error" role="alert">${escapeHTML(this._error)}</p>` : ''}
          ${this._greeted ? `<p class="quickstart-greeted" role="status">${escapeHTML(this._greeted)}</p>` : ''}
        </section>

        <section class="k-card quickstart-panel">
          <div class="quickstart-panel-head">
            <h2 class="quickstart-panel-title">Greetings</h2>
            <span class="k-badge k-badge--muted">${this._items.length}</span>
          </div>
          <p class="quickstart-meta">Greet calls the provider's one verb through kcp, authorized as you.</p>
          ${this._renderList()}
        </section>
      </div>
    `
    this._bind()
  }

  private _renderList(): string {
    if (!this._ctx?.tenant) return `<p class="quickstart-empty">Select a workspace to see its greetings.</p>`
    if (!this._loaded) return `<p class="quickstart-empty">Loading…</p>`
    if (this._items.length === 0) return `<p class="quickstart-empty">No greetings yet. Create one.</p>`
    return `<ul class="quickstart-list">${this._items.map(item => this._renderRow(item)).join('')}</ul>`
  }

  private _renderRow(item: Greeting): string {
    const name = item.metadata?.name || ''
    const ready = item.status?.conditions?.find(c => c.type === 'Ready')
    const isReady = ready?.status === 'True'
    // status.observedAt is the proof a reconciler is running: it is absent
    // until the provider's controller has seen the object.
    const observed = item.status?.observedAt ? new Date(item.status.observedAt).toLocaleString() : 'not observed yet'
    return `
      <li class="quickstart-row">
        <button class="k-btn k-btn--text quickstart-row-name" type="button" data-open="${escapeHTML(name)}">${escapeHTML(name)}</button>
        <span class="quickstart-row-message">${escapeHTML(item.spec?.message || '')}</span>
        <span class="k-badge ${isReady ? 'k-badge--success' : 'k-badge--warning'}" title="${escapeHTML(ready?.message || '')}">
          ${isReady ? 'Ready' : escapeHTML(ready?.reason || 'Pending')}
        </span>
        <span class="quickstart-row-observed">${escapeHTML(observed)}</span>
        <button class="k-btn" type="button" data-greet="${escapeHTML(name)}" ${this._busy === `greet:${name}` ? 'disabled' : ''}>
          ${ic('send')} Greet
        </button>
      </li>
    `
  }

  private _bind(): void {
    const form = this.querySelector<HTMLFormElement>('[data-form="create"]')
    form?.addEventListener('submit', event => {
      event.preventDefault()
      const data = new FormData(form)
      const name = String(data.get('name') || '').trim()
      const message = String(data.get('message') || '').trim()
      if (name && message) void this._create(name, message)
    })
    for (const button of this.querySelectorAll<HTMLButtonElement>('[data-greet]')) {
      button.addEventListener('click', () => void this._greet(button.dataset.greet as string))
    }
    for (const button of this.querySelectorAll<HTMLButtonElement>('[data-open]')) {
      button.addEventListener('click', () => this._openDetail(button.dataset.open as string))
    }
  }
}

export function escapeHTML(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
