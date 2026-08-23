// Connections tab: shared credentials for external systems. Each is a Tool
// agents call, a Channel they message you on, or a generic Connection.
// Type-driven create (CONN_DEFS carries the per-type setup guides), edit, test /
// enable-inbound / OAuth-connect actions. Toolsets live here as a section —
// both are "reusable capability config".

import { html, nothing, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { sliceView } from '../ui/states'
import { toast } from '../ui/toast'
import { confirmModal } from '../portalkit/modal'
import { mutate } from '../mutate'
import {
  CATEGORY_META,
  CONN_DEFS,
  connCategory,
  connShape,
  type ConnCategory,
  type ConnField,
  type ConnTypeDef,
} from '../conn-defs'
import type { Agent, Connection, ConnectionWrite } from '../types'

import './toolsets'
import './assisted-search'

// needsInstance flags a self-hosted search connection that names no instance.
// The websearch tool errors at runtime in that state and nothing else in the
// row shows it, so the table says so explicitly.
function needsInstance(c: Connection): boolean {
  return selfHostedSearch(c) && !c.spec.config?.instance
}

// selfHostedSearch is the websearch mode with no credential and no URL: it
// names an infrastructure instance and is reached over the data plane.
function selfHostedSearch(c: Connection): boolean {
  return c.spec.type === 'websearch' && c.spec.config?.provider === 'searxng'
}

// instanceBacked covers every connection addressed by instance name rather than
// by URL — self-hosted search and any MCP workload in this workspace. Those
// edit an instance name; everything else edits an endpoint and a secret.
function instanceBacked(c: Connection): boolean {
  return selfHostedSearch(c) || (c.spec.type === 'mcp' && !!c.spec.config?.instance)
}

// unwired flags a tool connection no agent has been granted. A tool family only
// exists for an agent that was wired the connection backing it, so an unwired
// search connection means every agent still answers "I can't make web
// requests" — a failure that is otherwise completely invisible from here.
function unwired(c: Connection, agents: Agent[]): boolean {
  if (connCategory(c.spec.type) !== 'tool') return false
  const name = c.metadata.name
  return !agents.some((a) => {
    const t = a.spec?.tools
    return (
      (t?.interactive?.connections || []).includes(name) ||
      (t?.background?.connections || []).includes(name) ||
      // A shared Toolset can carry the grant instead of the agent listing it.
      (t?.interactive?.toolsets || []).length > 0 ||
      (t?.background?.toolsets || []).length > 0
    )
  })
}

export class Connections extends StoreElement {
  @state() private connType: string | null = null
  @state() private connMode = ''
  @state() private editing: string | null = null

  private async create(def: ConnTypeDef, form: HTMLFormElement): Promise<void> {
    const v: Record<string, string> = {}
    form.querySelectorAll<HTMLInputElement>('input[name]').forEach((el) => (v[el.name] = el.value.trim()))
    const mode = this.connMode || def.modes?.[0].id || ''
    const res = await mutate(this.store, {
      run: () => this.api.createConnection(def.build(v, mode)),
      success: 'Connection created.',
      failure: 'Create failed',
      reload: ['connections'],
    })
    if (res) {
      this.connType = null
      this.connMode = ''
    }
  }

  private async saveEdit(c: Connection, form: HTMLFormElement, usesChannel: boolean): Promise<void> {
    const g = (n: string): string => (form.querySelector<HTMLInputElement>(`[name=${n}]`)?.value || '').trim()
    const patch: ConnectionWrite = { displayName: g('displayName') }
    // A self-hosted search connection is addressed by instance name, not by
    // URL — it is reached over the platform's internal data plane. config is
    // replaced wholesale by the patch endpoint, so provider must be re-sent.
    if (instanceBacked(c)) patch.config = { ...c.spec.config, instance: g('instance') }
    else if (usesChannel) patch.channel = g('endpoint')
    else patch.baseURL = g('endpoint')
    const secret = g('secret')
    if (secret) patch.secret = secret
    const res = await mutate(this.store, {
      run: () => this.api.patchConnection(c.metadata.name, patch),
      success: 'Connection updated.',
      failure: 'Update failed',
      reload: ['connections'],
    })
    if (res) this.editing = null
  }

  private async del(name: string): Promise<void> {
    const ok = await confirmModal({ title: `Delete connection “${name}”?`, danger: true, confirmLabel: 'Delete' })
    if (!ok) return
    await mutate(this.store, {
      run: () => this.api.deleteConnection(name),
      success: 'Connection deleted.',
      failure: 'Delete failed',
      reload: ['connections'],
    })
  }

  // Connection tests signal failure with an HTTP error (502 SendFailed, 400 for
  // a non-messaging type) carrying the reason in the status body, whereas
  // credential tests answer 200 with {ok:false, error} — see models.ts. Both
  // conventions are handled where they are; do not "align" one blindly.
  private async test(name: string): Promise<void> {
    await mutate(this.store, {
      run: () => this.api.testConnection(name),
      success: `Test message sent via ${name}. Check the channel.`,
      failure: `Test of “${name}” failed`,
    })
  }

  private async enableInbound(name: string): Promise<void> {
    const res = await mutate(this.store, {
      run: () => this.api.enableInbound(name),
      failure: 'Enable inbound failed',
      reload: ['connections'],
    })
    if (res) toast(res.registered ? 'ok' : 'info', `${res.note} ${res.webhookURL}`)
  }

  private async oauth(name: string): Promise<void> {
    const res = await mutate(this.store, { run: () => this.api.oauthAuthorize(name), failure: 'OAuth connect failed' })
    if (!res) return
    window.open(res.authorizeURL, '_blank', 'noopener')
    toast('info', 'Authorize in the opened tab, then refresh.')
  }

  render(): TemplateResult {
    return html`
      <div class="agents-menu">
        <div class="agents-panel k-card agents-route-panel">
          <h3>Connections</h3>
          <p class="muted">
            Shared credentials for external systems. Each is a ${icon('wrench')} <strong>Tool</strong> agents call, a
            ${icon('megaphone')} <strong>Channel</strong> they message you on, or a ${icon('plug')} generic
            <strong>Connection</strong>. Stored as Secrets in your workspace.
          </p>
          ${sliceView<Connection>({
            slice: this.store.connections,
            emptyIcon: 'plug',
            emptyText: 'No connections yet — add one below.',
            retry: () => void this.store.load('connections'),
            content: (rows) => this.table(rows),
          })}
          ${this.editorArea()}
        </div>
        <agents-toolsets .store=${this.store} .api=${this.api}></agents-toolsets>
      </div>
    `
  }

  private table(rows: Connection[]): TemplateResult {
    return html`<div class="agents-tablewrap k-table">
      <table class="agents-table">
        <thead>
          <tr><th>Name</th><th>Kind</th><th>Type</th><th>Endpoint / channel</th><th class="agents-th-actions">Actions</th></tr>
        </thead>
        <tbody>
          ${repeat(
            rows,
            (c) => c.metadata.name,
            (c) => {
              const cat = connCategory(c.spec.type)
              const meta = CATEGORY_META[cat]
              const name = c.metadata.name
              return html`<tr class=${this.editing === name ? 'is-editing' : ''}>
                <td>
                  <span class="agents-cell-name">${c.spec.displayName || name}</span>
                  ${c.status?.webhookPath ? html`<span class="agents-inbound-on" title="Inbound enabled">${icon('swap')}</span>` : nothing}
                  ${c.status?.oauthConnected ? html`<span class="agents-inbound-on" title="OAuth connected">${icon('link')}</span>` : nothing}
                </td>
                <td><span class="k-badge agents-badge agents-badge-cat agents-cat-${cat}">${icon(meta.icon)} ${meta.label}</span></td>
                <td><span class="k-badge agents-badge">${connShape(c).typeLabel}</span></td>
                <td class="agents-cell-task muted">
                  ${needsInstance(c)
                    ? html`<button
                        class="k-badge agents-badge k-badge--warning agents-badge-warn agents-badge-btn"
                        title="Edit this connection and name the searxng instance it should search through"
                        @click=${() => {
                          this.editing = name
                          this.connType = null
                        }}
                      >
                        ${icon('pencil')} needs an instance
                      </button>`
                    : c.spec.config?.instance || c.spec.baseURL || c.spec.channel || '—'}
                  ${unwired(c, this.store.agents.data)
                    ? html`<span class="k-badge agents-badge k-badge--warning agents-badge-warn" title="No agent has been granted this tool — add it under an agent's Config → Tools"
                        >not wired to an agent</span
                      >`
                    : nothing}
                </td>
                <td class="agents-row-actions">
                  <button
                    class="k-btn k-btn--ghost agents-iconbtn"
                    aria-label="Edit ${name}"
                    title="Edit"
                    @click=${() => {
                      this.editing = name
                      this.connType = null
                    }}
                  >
                    ${icon('pencil')}
                  </button>
                  ${cat === 'channel'
                    ? html`<button class="k-btn k-btn--ghost agents-iconbtn" aria-label="Send a test message via ${name}" title="Send a test message" @click=${() => void this.test(name)}>
                          ${icon('send')}
                        </button>
                        <button
                          class="k-btn k-btn--ghost agents-iconbtn"
                          aria-label="Enable inbound chat for ${name}"
                          title=${c.status?.webhookPath ? 'Inbound enabled' : 'Enable inbound chat'}
                          @click=${() => void this.enableInbound(name)}
                        >
                          ${icon('swap')}
                        </button>`
                    : nothing}
                  ${c.spec.auth === 'oauth'
                    ? html`<button
                        class="k-btn k-btn--ghost agents-iconbtn"
                        aria-label="Connect OAuth for ${name}"
                        title=${c.status?.oauthConnected ? 'Reconnect OAuth' : 'Connect OAuth'}
                        @click=${() => void this.oauth(name)}
                      >
                        ${icon('link')}
                      </button>`
                    : nothing}
                  <button class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger" aria-label="Delete ${name}" title="Delete" @click=${() => void this.del(name)}>
                    ${icon('trash')}
                  </button>
                </td>
              </tr>`
            },
          )}
        </tbody>
      </table>
    </div>`
  }

  // ---- create / edit forms -------------------------------------------------

  private editorArea(): TemplateResult {
    const editConn = this.editing ? this.store.connections.data.find((c) => c.metadata.name === this.editing) : undefined
    if (editConn) return this.editForm(editConn)
    const def = this.connType ? CONN_DEFS.find((d) => d.id === this.connType) : null
    if (def) return this.createForm(def)
    return this.picker()
  }

  private picker(): TemplateResult {
    const tile = (d: ConnTypeDef): TemplateResult => html`<button
      class="k-btn k-btn--ghost agents-conn-tile"
      @click=${() => {
        this.connType = d.id
        this.connMode = ''
      }}
    >
      <span class="agents-conn-glyph">${icon(d.glyph)}</span>
      <span class="agents-conn-name">${d.label}</span>
      <span class="muted">${d.desc}</span>
    </button>`
    return html`<div class="agents-conn-picker">
      <h4>Add a connection</h4>
      ${(['tool', 'channel', 'connection'] as ConnCategory[]).map((cat) => {
        const defs = CONN_DEFS.filter((d) => connCategory(d.id) === cat)
        if (!defs.length) return nothing
        const m = CATEGORY_META[cat]
        return html`<div class="agents-conn-group">
          <h5 class="agents-conn-grouphead">${icon(m.icon)} ${m.label}s <span class="muted">— ${m.blurb}</span></h5>
          <div class="agents-conn-types">${defs.map(tile)}</div>
          ${cat === 'tool'
            ? html`<agents-assisted-search .store=${this.store} .api=${this.api}></agents-assisted-search>`
            : nothing}
        </div>`
      })}
    </div>`
  }

  private field(f: ConnField): TemplateResult {
    return html`<label>
      ${f.label}${f.required ? ' *' : ''}
      <input class="k-input"
        name=${f.key}
        type=${f.password ? 'password' : 'text'}
        placeholder=${f.placeholder || ''}
        ?required=${f.required}
        autocomplete="off"
      />
      ${f.hint ? html`<span class="agents-hint">${f.hint}</span>` : nothing}
    </label>`
  }

  private createForm(def: ConnTypeDef): TemplateResult {
    const mode = this.connMode || def.modes?.[0].id || ''
    const activeMode = def.modes?.find((m) => m.id === mode)
    let fields = def.modes ? activeMode!.fields : def.fields || []
    const advanced = [...(def.advanced || []), ...(activeMode?.advanced || [])]
    // Platform OAuth app configured (operator env)? Then OAuth modes need no
    // client id/secret — drop those fields.
    const isOAuthMode = fields.some((f) => f.key === 'clientID')
    const platformApp = isOAuthMode && this.store.oauthApps.has(def.id)
    if (platformApp) fields = fields.filter((f) => f.key !== 'clientID' && f.key !== 'clientSecret')
    return html`<form
      class="agents-conn-form k-card"
      @submit=${(e: Event) => {
        e.preventDefault()
        void this.create(def, e.target as HTMLFormElement)
      }}
    >
      <div class="agents-conn-form k-cardhead">
        <button type="button" class="k-btn k-btn--ghost agents-back" @click=${() => (this.connType = null)}>${icon('arrow-left')} connection types</button>
        <h4>${icon(def.glyph)} ${def.label}</h4>
      </div>
      <p class="muted">${def.desc}</p>
      ${def.setup
        ? html`<details class="agents-setup" open>
            <summary>Before you start — setup steps</summary>
            <ol>
              ${def.setup.map((s) => html`<li>${unsafeHTML(s)}</li>`)}
            </ol>
          </details>`
        : nothing}
      <label>
        Name *
        <input class="k-input" name="name" required pattern="[a-z0-9-]+" placeholder=${`my-${def.id}`} autocomplete="off" />
        <span class="agents-hint">A short id you'll reference from agents.</span>
      </label>
      ${def.modes
        ? html`<div class="agents-modeseg" role="group" aria-label="Authentication mode">
            ${def.modes.map(
              (m) => html`<button type="button" class="k-btn k-btn--ghost agents-modebtn ${m.id === mode ? 'sel' : ''}" @click=${() => (this.connMode = m.id)}>
                ${m.label}
              </button>`,
            )}
          </div>`
        : nothing}
      ${platformApp
        ? html`<div class="agents-platform-note">
            ${icon('check')} Using the platform's ${def.label} OAuth app — no client id/secret needed. Create it, then click
            <strong>Connect</strong>.
          </div>`
        : nothing}
      ${fields.map((f) => this.field(f))}
      ${advanced.length
        ? html`<details class="agents-adv"><summary>Advanced</summary>${advanced.map((f) => this.field(f))}</details>`
        : nothing}
      <div><button class="k-btn k-btn--primary" type="submit">Create connection</button></div>
    </form>`
  }

  private editForm(c: Connection): TemplateResult {
    const cat = connCategory(c.spec.type)
    const usesChannel = cat === 'channel' || !!c.spec.channel
    const shape = connShape(c)
    let endpointLabel: string
    if (shape.discordWebhook) endpointLabel = 'Webhook URL'
    else if (shape.discordBot) endpointLabel = 'Channel ID (optional)'
    else if (!usesChannel) endpointLabel = 'Endpoint URL'
    else if (c.spec.type === 'slack') endpointLabel = 'Webhook URL / channel'
    else if (c.spec.type === 'smtp') endpointLabel = 'Send to'
    else endpointLabel = 'Channel / chat ID'
    const isOAuth = c.spec.auth === 'oauth'
    return html`<form
      class="agents-conn-form k-card"
      @submit=${(e: Event) => {
        e.preventDefault()
        void this.saveEdit(c, e.target as HTMLFormElement, usesChannel)
      }}
    >
      <div class="agents-conn-form k-cardhead">
        <button type="button" class="k-btn k-btn--ghost agents-back" @click=${() => (this.editing = null)}>${icon('arrow-left')} connections</button>
        <h4>
          Edit ${icon(CATEGORY_META[cat].icon)} <code>${c.metadata.name}</code>
          ${c.spec.type === 'discord' ? html`<span class="k-badge agents-badge">${shape.typeLabel}</span>` : nothing}
        </h4>
      </div>
      <label>Display name<input class="k-input" name="displayName" .value=${c.spec.displayName || ''} placeholder=${c.metadata.name} /></label>
      ${instanceBacked(c)
        ? html`<label>
            Instance name *
            <input class="k-input" name="instance" .value=${c.spec.config?.instance || ''} placeholder="search" required autocomplete="off" />
            <span class="agents-hint">
              The instance under Infrastructure. Agents reach it over the platform's internal path — there is no URL and no token.
            </span>
          </label>`
        : html`<label>${endpointLabel}<input class="k-input" name="endpoint" .value=${(usesChannel ? c.spec.channel : c.spec.baseURL) || ''} /></label>`}
      ${instanceBacked(c)
        ? nothing
        : shape.discordWebhook
        ? nothing
        : isOAuth
          ? html`<p class="agents-hint">
              This is an OAuth connection — use the ${icon('link')} button in the table to re-authorize. Client credentials aren't
              edited here.
            </p>`
          : html`<label>
              New ${shape.discordBot ? 'bot token' : 'secret / token'}
              <input class="k-input" name="secret" type="password" placeholder="leave blank to keep the current one" autocomplete="off" />
              <span class="agents-hint">Only set this to rotate the credential.</span>
            </label>`}
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="submit">Save changes</button>
        <button type="button" class="k-btn k-btn--ghost secondary" @click=${() => (this.editing = null)}>Cancel</button>
      </div>
    </form>`
  }
}

if (!customElements.get('agents-connections')) customElements.define('agents-connections', Connections)
