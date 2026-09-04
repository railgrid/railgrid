// "Set up self-hosted search with an agent" — the assisted path through the
// three-step websearch/searxng dance.
//
// It is a convenience sitting next to the normal create form, never a
// replacement: it renders at all only when (a) capabilities say infrastructure
// federates into this workspace's tool surface, so an agent really can call
// provision, and (b) there is at least one agent to drive it. Either missing
// and this component is invisible — including when the capability probe itself
// failed, because a broken assisted flow is worse than no assisted flow.
//
// What it does: create the Connection naming the instance-to-be, then hand the
// agent a prompt and drop the user into its chat to watch the work happen.
// There is no separate progress surface on purpose: the chat already streams
// tool calls.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { StoreElement, type AuthoritySnapshot } from '../ui/base'
import { icon } from '../ui/icon'
import { mutate } from '../mutate'
import type { CreateSuccessDetail } from '../router'
import {
  DNS_LABEL_RE,
  INSTANCE_SIZES,
  assistDismissed,
  rememberAssistDismissed,
  searxngSetupPrompt,
  selfHostedSearchConfigured,
  type InstanceSize,
} from '../assisted-setup'
import { readTenant } from '../portalkit/tenant'
import { familiesForConns } from '../conn-defs'
import type { ConnectionWrite } from '../types'

export class AssistedSearch extends StoreElement {
  @property({ type: Boolean }) page = false
  @state() private agent = ''
  @state() private connName = 'search'
  @state() private instance = ''
  @state() private size: InstanceSize = 'small'
  @state() private errors: Record<string, string> = {}
  @state() private busy = false

  // instanceTouched tracks whether the user has edited the instance name. Until
  // they do it mirrors the connection name, which is the default the two share.
  private instanceTouched = false

  private cancel(): void {
    if (this.busy) return
    this.dispatchEvent(new CustomEvent('agents-cancel', { bubbles: true, composed: true }))
  }

  private selectedAgent(): string {
    return this.agent || this.store.agents.data[0]?.metadata.name || ''
  }

  private instanceName(): string {
    return (this.instanceTouched ? this.instance : this.connName).trim()
  }

  private validate(conn: string, inst: string): Record<string, string> {
    const errors: Record<string, string> = {}
    if (!conn) errors.connName = 'A name is required.'
    else if (!DNS_LABEL_RE.test(conn)) errors.connName = 'Lowercase letters, digits and dashes only.'
    else if (this.store.connections.data.some((c) => c.metadata.name === conn))
      errors.connName = 'A connection with that name already exists.'
    if (!inst) errors.instance = 'A name is required.'
    else if (!DNS_LABEL_RE.test(inst)) errors.instance = 'Lowercase letters, digits and dashes only.'
    if (!this.agent) errors.agent = 'Pick the agent that should do this.'
    return errors
  }

  private async submit(e: Event): Promise<void> {
    e.preventDefault()
    if (this.busy) return
    const conn = this.connName.trim()
    const inst = this.instanceName()
    const agent = this.selectedAgent()
    const size = this.size
    const authority = this.captureAuthority()
    this.agent = agent
    const errors = this.validate(conn, inst)
    this.errors = errors
    if (Object.keys(errors).length) return

    // The Connection carries no credential and no URL: it names the instance,
    // and search reaches that instance over the platform's internal data plane.
    // Creating it before the instance exists is fine — the name is the only
    // binding, and search reports a clear error until the instance is ready.
    const body: ConnectionWrite = {
      type: 'websearch',
      name: conn,
      config: { provider: 'searxng', instance: inst },
    }
    this.busy = true
    try {
      const res = await mutate(authority.store, {
        run: () => authority.api.createConnection(body),
        success: `Connection “${conn}” created — handing the rest to ${agent}.`,
        failure: 'Create failed',
        reload: ['connections'],
      })
      // A context rotation can leave this light-DOM child alive until the
      // shell's keyed route update flushes. Do not grant, hand off, or route a
      // create response into a different authority in that gap.
      if (!res || !this.authorityIsCurrent(authority)) return

      await this.grantToAgent(agent, conn, authority)
      if (!this.authorityIsCurrent(authority)) return

      authority.store.setPendingPrompt(agent, searxngSetupPrompt({ connection: conn, instance: inst, size }))
      this.dispatchEvent(
        new CustomEvent<CreateSuccessDetail>('agents-create-success', {
          detail: {
            resource: 'connection',
            name: conn,
            item: res,
            // Assisted setup intentionally keeps its existing handoff: the
            // agent's Config/chat surface is where the provisioning prompt is
            // actionable and visible.
            destination: { kind: 'agent', name: agent, tab: 'config' },
          },
          bubbles: true,
          composed: true,
        }),
      )
    } finally {
      this.busy = false
    }
  }

  // grantToAgent wires the new connection into the driving agent's interactive
  // tools. Without this the flow ends with a search backend the agent cannot
  // actually use: `web_search` only exists when a websearch connection is
  // granted, so the agent would provision its own backend and still answer
  // "I can't make web requests". Failure is surfaced but not fatal — the
  // connection and the instance are still worth having.
  private async grantToAgent(agentName: string, conn: string, authority: AuthoritySnapshot): Promise<void> {
    const agent = authority.store.agents.data.find((a) => a.metadata.name === agentName)
    if (!agent) return
    const current = agent.spec?.tools?.interactive?.connections || []
    if (current.includes(conn)) return
    const connections = [...current, conn]
    // Families are derived from the wired connections, never hand-picked. The
    // connection just created is resolved locally: the store slice may not have
    // refreshed yet, and without its type the `web` family would be dropped —
    // leaving the agent with a search backend but no web_search tool.
    const families = familiesForConns(connections, (n) =>
      n === conn ? 'websearch' : authority.store.connections.data.find((c) => c.metadata.name === n)?.spec.type,
    )
    await mutate(authority.store, {
      run: () => authority.api.patchAgent(agentName, { interactiveConnections: connections, interactiveFamilies: families }),
      success: `Wired “${conn}” into ${agentName}'s tools.`,
      failure: `Created the connection, but could not wire it into ${agentName} — add it under the agent's Tools`,
      reload: ['agents'],
    })
  }

  render(): TemplateResult {
    if (this.page) {
      // A direct/bookmarked assisted route should remain truthful when its
      // prerequisites disappear; it must not offer a form that cannot hand
      // work to an agent.
      if (!this.store.hasProvider('infrastructure')) return html`<div class="agents-panel k-card agents-route-panel"><p class="muted">Infrastructure is not enabled for this workspace.</p></div>`
      if (!this.store.agents.data.length) return html`<div class="agents-panel k-card agents-route-panel"><p class="muted">Create an agent before using assisted search setup.</p></div>`
      if (!this.store.connections.loaded) return html`<div class="agents-panel k-card agents-route-panel k-loading-reveal" role="status"><p class="muted">Loading connections…</p></div>`
      if (selfHostedSearchConfigured(this.store.connections.data)) {
        return html`<div class="agents-panel k-card agents-route-panel"><p class="muted">Self-hosted search is already configured in this workspace.</p></div>`
      }
      return this.createPage()
    }
    // Both gates matter: no infrastructure tools means the agent cannot
    // provision anything, no agents means there is nobody to ask.
    if (!this.store.hasProvider('infrastructure') || !this.store.agents.data.length) return html`${nothing}`
    // Wait for connections before deciding. Rendering first and hiding on the
    // next tick would flash an onboarding card at users who finished
    // onboarding long ago.
    if (!this.store.connections.loaded) return html`${nothing}`
    if (selfHostedSearchConfigured(this.store.connections.data)) return html`${nothing}`
    if (this.dismissed) return html`${nothing}`
    return html`${this.card()}`
  }

  // dismissed mirrors the stored flag. Held in @state so dismissing re-renders
  // immediately rather than waiting for the next store update.
  @state() private dismissed = assistDismissed(readTenant().workspaceUUID)

  private dismiss(): void {
    rememberAssistDismissed(readTenant().workspaceUUID)
    this.dismissed = true
  }

  private card(): TemplateResult {
    return html`<div class="agents-assist">
      <span class="agents-assist-ic">${icon('sparkles')}</span>
      <div class="agents-assist-body">
        <strong>Set up self-hosted search with an agent</strong>
        <span class="muted"
          >One of your agents can provision the SearXNG instance for you — instead of you hopping to Infrastructure and back.</span
        >
      </div>
      <button
        type="button"
        class="k-btn k-btn--ghost secondary"
        @click=${() => this.navigate({ kind: 'create', resource: 'connection', type: 'assisted-search' })}
      >Set it up</button>
      <button
        type="button"
        class="k-btn k-btn--ghost agents-iconbtn"
        aria-label="Dismiss this suggestion"
        title="Dismiss — you can still add a web-search connection above"
        @click=${() => this.dismiss()}
      >
        ${icon('x')}
      </button>
    </div>`
  }

  private createPage(): TemplateResult {
    const agents = this.store.agents.data
    const selected = this.selectedAgent()
    const form = html`<form
        class="agents-conn-form agents-guided-form k-create-surface"
        aria-label="Assisted search setup"
        @submit=${(e: Event) => void this.submit(e)}
      >
        <div class="k-create-body">
        <p class="muted">
          We create the web-search connection pointing at an instance name, then your agent provisions the <code>searxng</code>
          instance itself. Nothing to paste back: agents reach it over the platform's internal path, so it is never published and
          has no token.
        </p>

        ${agents.length > 1
          ? html`<label>
              Agent
              <select
                class="k-input"
                ?disabled=${this.busy}
                aria-invalid=${this.errors.agent ? 'true' : nothing}
                @change=${(e: Event) => {
                  if (this.busy) return
                  this.agent = (e.target as HTMLSelectElement).value
                }}
              >
                ${agents.map((a) => html`<option value=${a.metadata.name} ?selected=${a.metadata.name === selected}>${a.spec?.displayName || a.metadata.name}</option>`)}
              </select>
              ${this.errors.agent ? html`<span class="agents-fielderr">${this.errors.agent}</span>` : nothing}
            </label>`
          : html`<p class="agents-hint">Driven by your agent <strong>${selected}</strong>.</p>`}

        <label>
          Connection name
          <input class="k-input"
            name="connName"
            .value=${this.connName}
            ?disabled=${this.busy}
            autocomplete="off"
            aria-invalid=${this.errors.connName ? 'true' : nothing}
            @input=${(e: Event) => {
              if (!this.busy) this.connName = (e.target as HTMLInputElement).value
            }}
          />
          ${this.errors.connName
            ? html`<span class="agents-fielderr">${this.errors.connName}</span>`
            : html`<span class="agents-hint">What agents reference to search the web.</span>`}
        </label>

        <label>
          Instance name
          <input class="k-input"
            name="instance"
            .value=${this.instanceName()}
            ?disabled=${this.busy}
            autocomplete="off"
            aria-invalid=${this.errors.instance ? 'true' : nothing}
            @input=${(e: Event) => {
              if (this.busy) return
              this.instanceTouched = true
              this.instance = (e.target as HTMLInputElement).value
            }}
          />
          ${this.errors.instance
            ? html`<span class="agents-fielderr">${this.errors.instance}</span>`
            : html`<span class="agents-hint">The infrastructure instance the agent provisions.</span>`}
        </label>

        <label>
          Size
          <div class="agents-modeseg" role="group" aria-label="Instance size">
            ${INSTANCE_SIZES.map(
              (s) => html`<button
                type="button"
                ?disabled=${this.busy}
                class="k-btn k-btn--ghost agents-modebtn ${s === this.size ? 'sel' : ''}"
                @click=${() => {
                  if (!this.busy) this.size = s
                }}
              >${s}</button>`,
            )}
          </div>
        </label>
        </div>

        <div class="k-create-actions">
          <button type="button" ?disabled=${this.busy} class="k-btn k-btn--ghost secondary" @click=${() => this.cancel()}>Cancel</button>
          <button class="k-btn k-btn--primary" type="submit" ?disabled=${this.busy}>${icon('sparkles')} Create and hand off</button>
        </div>
      </form>`
    return html`<div class="agents-create-page k-create-page">
      <button type="button" ?disabled=${this.busy} class="k-btn k-btn--ghost k-back-action" @click=${() => this.cancel()}>${icon('arrow-left')} Connections</button>
      <header class="k-create-header"><h1 class="k-create-title">Set up assisted search</h1><p class="k-create-description">Create a private search connection and let an agent provision the supporting service.</p></header>
      ${form}
    </div>`
  }
}

if (!customElements.get('agents-assisted-search')) customElements.define('agents-assisted-search', AssistedSearch)
