// QuickstartElement is the custom element the kedge portal renders for
// this provider. The portal:
//   1. Loads main.js via a one-shot <script> tag (this module's side effect
//      registers the element with customElements.define).
//   2. Appends the element to its DOM tree.
//   3. Sets element.kedgeContext as a JS property (NOT an attribute) — the
//      setter triggers a re-render.
//   4. Listens for kedge-navigate CustomEvents bubbled from the element to
//      push browser history within the portal SPA.
//
// The element runs in light DOM so the portal's :root CSS variables cascade
// in — see style.css.

// KedgeContext is the shape the host portal sets on element.kedgeContext
// once mounted. Fields are optional because the portal may push partial
// updates (theme toggle, token rotation) and our render must cope.
export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

interface APIState {
  cls: 'ok' | 'warn'
  label: string
  dump: string
}

export class QuickstartElement extends HTMLElement {
  private _ctx: KedgeContext | null = null
  private _calledAPI = false
  private _apiState: APIState | null = null

  // Portal sets this property after appending; setter triggers a render so
  // the panels reflect the new identity/theme without an explicit refresh.
  set kedgeContext(v: KedgeContext | null) {
    this._ctx = v
    this._render()
  }
  get kedgeContext(): KedgeContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._render()
    this._callAPI()
  }

  private _render(): void {
    const ctx = this._ctx
    const apiStateDump = this._apiState ? this._apiState.dump : '(no response yet)'
    const apiStateLabel = this._apiState ? this._apiState.label : 'Calling…'
    const ctxDump = ctx
      ? JSON.stringify(
          { user: ctx.user, tenant: ctx.tenant, theme: ctx.theme, basePath: ctx.basePath },
          null,
          2,
        )
      : '(no context yet)'

    this.innerHTML = `
      <div class="grid">
        <div class="panel">
          <div class="panel-head">
            <h2 class="panel-title">Portal handshake</h2>
            <span class="badge ${ctx ? 'ok' : 'warn'}">${ctx ? 'Connected' : 'Waiting'}</span>
          </div>
          <p class="meta">Context delivered by the shell as a JS property — no postMessage shuttle.</p>
          <pre class="${ctx ? '' : 'muted'}" id="ctx-dump">${escapeHTML(ctxDump)}</pre>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h2 class="panel-title">Backend proxy</h2>
            <span class="badge warn" id="api-status">${escapeHTML(apiStateLabel)}</span>
          </div>
          <p class="meta">GET <code id="api-url">${escapeHTML(this._apiURL())}</code></p>
          <pre class="${this._apiState ? '' : 'muted'}" id="api-dump">${escapeHTML(apiStateDump)}</pre>
        </div>
      </div>
    `

    // The status badge's class is overwritten by the innerHTML template
    // above; restore the resolved color class once the API call settles.
    if (this._apiState) {
      const s = this.querySelector<HTMLElement>('#api-status')
      if (s) s.className = 'badge ' + this._apiState.cls
    }
  }

  // The backend proxy lives at /services/providers/{name}/* — derive the
  // URL from the basePath the portal hands us so the same code works when
  // running behind a non-default mount point.
  private _apiURL(): string {
    const base = (this._ctx?.basePath || '').replace(/^\/ui\/providers\//, '/services/providers/')
    return base + '/api/hello'
  }

  private _callAPI(): void {
    if (this._calledAPI) return
    this._calledAPI = true
    // basePath may arrive on a later kedgeContext set; poll briefly so we
    // don't issue the call against a partial URL on the first paint.
    const tryFetch = () => {
      if (!this._ctx?.basePath) {
        setTimeout(tryFetch, 50)
        return
      }
      const url = this._apiURL()
      fetch(url, { credentials: 'same-origin' })
        .then((r) => r.json().then((j) => ({ ok: r.ok, code: r.status, j })))
        .then(({ ok, code, j }) => {
          this._apiState = ok
            ? { cls: 'ok', label: code + ' OK', dump: JSON.stringify(j, null, 2) }
            : { cls: 'warn', label: code + ' ' + (j.reason || 'Error'), dump: JSON.stringify(j, null, 2) }
          this._render()
        })
        .catch((e: Error) => {
          this._apiState = { cls: 'warn', label: 'Error', dump: 'fetch error: ' + e.message }
          this._render()
        })
    }
    tryFetch()
  }
}

function escapeHTML(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
