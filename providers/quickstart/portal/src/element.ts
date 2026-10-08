import { createKubeClient, type KubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import { ic } from './portalkit/icons'
import { providerFetch, type ProviderFetch } from './portalkit/tenant'
import { toast } from './portalkit/toast'

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
  user?: { email?: string; sub?: string; userId?: string } | null
  // tenant is the kcp logical-cluster ID of the active workspace. It is what
  // addresses kcp (/clusters/{tenant}) — the bound CRs and the data-plane
  // verb alike, because a verb is a kcp custom subresource on the same path.
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  // basePath is this provider's UI mount, /ui/providers/quickstart. This
  // element does not need it: the hub's backend proxy under it is only for a
  // provider's /oauth and /mcp routes, and the quickstart serves neither.
  basePath?: string
  subPath?: string
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
  private _loading = false
  private _readError = ''
  private _error = ''
  private _busy = ''
  private _lastKey = ''
  private _draftName = ''
  private _draftMessage = ''
  private _generation = 0
  private _readSerial = 0
  private _active = false
  private _greetPending = new Set<string>()
  private _greetResults = new Map<string, { message: string; error: boolean }>()

  // The host sets this after appending and again on every change. Re-render,
  // and reload when the identity of the data actually changed.
  set railgridContext(v: RailgridContext | null) {
    this._ctx = v
    const hasHostFetch = typeof v?.fetch === 'function'
    const key = `${v?.tenant ?? ''}|${v?.user?.userId ?? v?.user?.sub ?? v?.user?.email ?? ''}|${hasHostFetch}|${hasHostFetch ? '' : v?.token ?? ''}`
    const changed = key !== this._lastKey
    this._lastKey = key
    if (changed) {
      this._generation += 1
      this._readSerial += 1
      this._items = []
      this._loaded = false
      this._loading = false
      this._readError = ''
      this._error = ''
      this._busy = ''
      this._greetPending.clear()
      this._greetResults.clear()
      this._draftName = ''
      this._draftMessage = ''
    }
    this._render()
    if (changed && this._canLoad()) void this._load()
  }
  get railgridContext(): RailgridContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._active = true
    this._render()
    if (this._canLoad()) void this._load()
  }

  disconnectedCallback(): void {
    this._active = false
    this._generation += 1
    this._readSerial += 1
    this._busy = ''
    this._greetPending.clear()
    this._loading = false
  }

  private _current(generation: number): boolean {
    return this._active && generation === this._generation
  }

  // A workspace and a transport are the two preconditions for any request. The
  // host may push a partial context first, so this is a guard, not a wait:
  // the setter calls back when the real one lands.
  private _canLoad(): boolean {
    return !!this._ctx?.tenant
  }

  private _kube(ctx = this._ctx): KubeClient {
    return createKubeClient({
      fetch: providerFetch(ctx),
      cluster: ctx?.tenant as string,
      fieldManager: 'railgrid-provider-quickstart',
    })
  }

  private async _load(): Promise<void> {
    if (!this._canLoad()) return
    const generation = this._generation
    const serial = ++this._readSerial
    const ctx = this._ctx
    this._loading = true
    this._readError = ''
    this._render()
    try {
      const list = await this._kube(ctx).list<Greeting>(greetings)
      if (!this._current(generation) || serial !== this._readSerial) return
      this._items = list.items
      this._loaded = true
    } catch (err) {
      if (!this._current(generation) || serial !== this._readSerial) return
      this._readError = (err as Error).message
    } finally {
      if (this._current(generation) && serial === this._readSerial) {
        this._loading = false
        this._render()
      }
    }
  }

  private async _create(name: string, message: string): Promise<void> {
    if (this._busy || !this._canLoad()) return
    const generation = this._generation
    const ctx = this._ctx
    this._busy = 'create'
    this._render()
    try {
      await this._kube(ctx).create<Greeting>(greetings, {
        apiVersion: `${greetings.group}/${greetings.version}`,
        kind: 'Greeting',
        metadata: { name },
        spec: { message },
      })
      if (!this._current(generation)) return
      this._draftName = ''
      this._draftMessage = ''
      this._error = ''
      await this._load()
      if (this._current(generation)) {
        toast('ok', `Greeting ${name} created.`)
        this._navigate('')
      }
    } catch (err) {
      if (!this._current(generation)) return
      this._error = (err as Error).message
    } finally {
      if (!this._current(generation)) return
      this._busy = ''
      this._render()
      this.querySelector<HTMLInputElement>('input[name="name"]')?.focus()
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
    if (!ctx?.tenant || this._greetPending.has(name)) return
    const generation = this._generation
    this._greetPending.add(name)
    this._greetResults.delete(name)
    this._render()
    try {
      const url = this._kube(ctx).verbPath(greetings, name, 'greet')
      const res = await providerFetch(ctx)(url, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ input: {} }),
      })
      const envelope = (await res.json()) as { result?: { greeting?: string }; error?: { message?: string } }
      if (!this._current(generation)) return
      this._greetResults.set(name, {
        message: res.ok ? envelope.result?.greeting || 'Greeting completed.' : envelope.error?.message || `Greet failed with HTTP ${res.status}. Try again.`,
        error: !res.ok,
      })
    } catch (err) {
      if (!this._current(generation)) return
      this._greetResults.set(name, { message: (err as Error).message, error: true })
    } finally {
      if (!this._current(generation)) return
      this._greetPending.delete(name)
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

  private _navigate(path: string): void {
    this.dispatchEvent(new CustomEvent('railgrid-navigate', { detail: { path }, bubbles: true }))
  }

  private _render(): void {
    const ctx = this._ctx
    const focused = this.querySelector<HTMLInputElement>('input:focus')
    const focusName = focused?.name
    const selection = focused ? [focused.selectionStart, focused.selectionEnd] as const : null
    const createRoute = (ctx?.subPath || '').replace(/^\/+|\/+$/g, '') === 'create/greeting'
    const firstRun = !!ctx?.tenant && this._loaded && this._items.length === 0
    this.innerHTML = createRoute ? `
      <section class="k-create-page">
        <button class="k-btn k-btn--ghost k-back-action" type="button" data-cancel ${this._busy ? 'disabled' : ''}>${ic('arrow-left')} Back to greetings</button>
        <header class="k-create-header">
          <h2 class="k-create-title">New greeting</h2>
          <p class="k-create-description">Create a greeting in the selected workspace, then send it from the greetings list.</p>
        </header>
        <form class="k-create-surface" data-form="create" aria-busy="${this._busy === 'create'}">
          <div class="k-create-body">
            ${!ctx?.tenant ? '<p class="quickstart-meta" role="status">Select a workspace before creating a greeting.</p>' : ''}
            <label class="quickstart-field">
              <span class="quickstart-label">Name <span>(required)</span></span>
              <input class="k-input" name="name" required pattern="[a-z0-9]((?:[a-z0-9]|-)*[a-z0-9])?" placeholder="hello" aria-describedby="quickstart-name-hint" value="${escapeHTML(this._draftName)}" ${this._busy === 'create' ? 'readonly' : ''} />
              <span id="quickstart-name-hint" class="quickstart-meta">Use lowercase letters, numbers, and hyphens.</span>
            </label>
            <label class="quickstart-field">
              <span class="quickstart-label">Message <span>(required)</span></span>
              <input class="k-input" name="message" required maxlength="256" placeholder="Hello there" value="${escapeHTML(this._draftMessage)}" ${this._busy === 'create' ? 'readonly' : ''} />
            </label>
            ${this._error ? `<p class="quickstart-error" role="alert">${escapeHTML(this._error)}</p>` : ''}
            ${this._busy === 'create' ? '<span class="quickstart-read-status" role="status">Creating greeting…</span>' : ''}
          </div>
          <div class="k-create-actions">
            <button class="k-btn k-btn--ghost" type="button" data-cancel ${this._busy ? 'disabled' : ''}>Cancel</button>
            <button class="k-btn k-btn--primary" type="submit" ${ctx?.tenant && !this._busy ? '' : 'disabled'}>${ic('plus')} ${this._busy === 'create' ? 'Creating…' : 'Create greeting'}</button>
          </div>
        </form>
      </section>
    ` : `
      <div class="quickstart-page">
        <header class="quickstart-page-head">
          <div><h2 class="quickstart-page-title">Greetings</h2><p class="quickstart-meta">Send a greeting to see its response. Each greeting belongs to this workspace.</p></div>
          ${!firstRun ? `<button class="k-btn k-btn--primary" type="button" data-create ${ctx?.tenant ? '' : 'disabled'}>${ic('plus')} New greeting</button>` : ''}
        </header>
        <section class="${firstRun ? 'quickstart-first-use' : 'k-card quickstart-panel'}" aria-label="Workspace greetings">
          <div class="quickstart-panel-head">
            <h3 class="quickstart-panel-title">Workspace greetings</h3>
            <span class="k-badge k-badge--muted">${this._items.length}</span>
            <button class="k-btn k-btn--ghost" type="button" data-refresh ${this._loading || !ctx?.tenant ? 'disabled' : ''}>${ic('refresh')} ${this._loading ? 'Refreshing…' : 'Refresh'}</button>
          </div>
          ${this._readError ? `<p class="quickstart-error" role="alert">${this._loaded ? 'Showing the last successful greetings. ' : ''}${escapeHTML(this._readError)} <button class="k-btn k-btn--ghost" type="button" data-refresh>Retry</button></p>` : ''}
          ${this._loading && this._loaded ? '<span class="quickstart-read-status" role="status">Refreshing greetings…</span>' : ''}
          ${this._renderList()}
        </section>
      </div>
    `
    this._bind()
    if (focusName) {
      const input = this.querySelector<HTMLInputElement>(`input[name="${focusName}"]`)
      input?.focus()
      if (input && selection && selection[0] !== null && selection[1] !== null) input.setSelectionRange(selection[0], selection[1])
    }
  }

  private _renderList(): string {
    if (!this._ctx?.tenant) return `<p class="quickstart-empty">Select a workspace to see its greetings.</p>`
    if (!this._loaded) return `<p class="quickstart-empty" role="status">${this._readError ? 'Greetings could not be loaded. Retry the inventory read.' : 'Loading greetings…'}</p>`
    if (this._items.length === 0) return `
      <section class="k-first-run" aria-labelledby="quickstart-first-run-title">
        <div class="k-first-run__lead">
          <span class="k-first-run__icon" aria-hidden="true">${ic('message')}</span>
          <div class="k-first-run__copy"><h3 id="quickstart-first-run-title">Create your first greeting</h3><p>Give your greeting a name and message, then send it to see a response.</p></div>
          <div class="k-first-run__actions"><button class="k-btn k-btn--primary" type="button" data-create>${ic('plus')} New greeting</button></div>
        </div>
        <ol class="k-first-run__journey" aria-label="Greeting setup">
          <li class="k-first-run__step is-current" aria-current="step"><span class="k-first-run__marker" aria-hidden="true">${ic('circle')}</span><span class="k-first-run__step-copy"><span class="k-first-run__step-status">Current step:</span><strong>Create a greeting</strong><small>Choose its name and message for this workspace.</small></span></li>
          <li class="k-first-run__step"><span class="k-first-run__marker" aria-hidden="true">2</span><span class="k-first-run__step-copy"><span class="k-first-run__step-status">Upcoming step:</span><strong>Send your greeting</strong><small>Select Greet from the list to see its response.</small></span></li>
        </ol>
      </section>`
    return `<ul class="quickstart-list">${this._items.map(item => this._renderRow(item)).join('')}</ul>`
  }

  private _renderRow(item: Greeting): string {
    const name = item.metadata?.name || ''
    const ready = item.status?.conditions?.find(c => c.type === 'Ready')
    const isReady = ready?.status === 'True'
    // status.observedAt is the proof a reconciler is running: it is absent
    // until the provider's controller has seen the object.
    const observed = item.status?.observedAt ? new Date(item.status.observedAt).toLocaleString() : 'not observed yet'
    const result = this._greetResults.get(name)
    const pending = this._greetPending.has(name)
    return `
      <li class="quickstart-row">
        <button class="k-btn k-btn--text quickstart-row-name" type="button" data-open="${escapeHTML(name)}">${escapeHTML(name)}</button>
        <span class="quickstart-row-message" title="${escapeHTML(item.spec?.message || '')}">${escapeHTML(item.spec?.message || '')}</span>
        <span class="k-badge ${isReady ? 'k-badge--success' : 'k-badge--warning'}" title="${escapeHTML(ready?.message || '')}">
          ${isReady ? 'Ready' : escapeHTML(ready?.reason || 'Pending')}
        </span>
        <span class="quickstart-row-observed">${escapeHTML(observed)}</span>
        <button class="k-btn k-btn--ghost" type="button" data-greet="${escapeHTML(name)}" ${pending ? 'disabled' : ''} aria-busy="${pending}" aria-label="${pending ? 'Greeting' : 'Greet'} ${escapeHTML(name)}">
          ${ic('send')} ${pending ? 'Greeting…' : 'Greet'}
        </button>
        ${pending ? `<span class="quickstart-row-result" role="status">Greeting ${escapeHTML(name)}…</span>` : ''}
        ${result ? `<span class="quickstart-row-result ${result.error ? 'quickstart-error' : 'quickstart-greeted'}" role="${result.error ? 'alert' : 'status'}">${escapeHTML(name)}: ${escapeHTML(result.message)}</span>` : ''}
      </li>
    `
  }

  private _bind(): void {
    for (const button of this.querySelectorAll<HTMLButtonElement>('[data-create]')) {
      button.addEventListener('click', () => this._navigate('create/greeting'))
    }
    for (const button of this.querySelectorAll<HTMLButtonElement>('[data-cancel]')) {
      button.addEventListener('click', () => this._navigate(''))
    }
    for (const button of this.querySelectorAll<HTMLButtonElement>('[data-refresh]')) {
      button.addEventListener('click', () => { void this._load() })
    }
    const form = this.querySelector<HTMLFormElement>('[data-form="create"]')
    form?.addEventListener('input', () => {
      this._draftName = form.querySelector<HTMLInputElement>('input[name="name"]')?.value || ''
      this._draftMessage = form.querySelector<HTMLInputElement>('input[name="message"]')?.value || ''
    })
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
