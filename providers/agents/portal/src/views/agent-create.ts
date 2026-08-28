// Agent creation wizard. The old create form was a bare name field, so every
// new agent landed in chat saying "no model assigned" — the one thing it needs
// to do anything. The model credential is required here; the system prompt and
// a primary channel are optional but offered because they are the next two
// things everyone sets.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { mutate } from '../mutate'
import type { CreateSuccessDetail } from '../router'
import type { AgentCreate } from '../types'

const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/

export class AgentCreateWizard extends StoreElement {
  // Route-owned creation renders as a normal page surface. The default keeps
  // the standalone element compatible with older embedders/tests that still
  // open the compact dialog directly.
  @property({ type: Boolean }) routeOwned = false
  @state() private name = ''
  @state() private modelCredential = ''
  @state() private systemPrompt = ''
  @state() private channel = ''
  // Capabilities, not a preset: the same two toggles the Config pane shows, so
  // there is one vocabulary for "what can this agent do" whether you are creating
  // it or editing it. Each carries its own behaviour (the provider injects the
  // fan-out mechanics with the grant), so ticking one is all that is needed.
  @state() private web = false
  @state() private fanOut = false
  @state() private errors: Record<string, string> = {}
  @state() private busy = false

  firstUpdated(): void {
    this.querySelector<HTMLInputElement>('input[name=name]')?.focus()
  }

  private async submit(e: Event): Promise<void> {
    e.preventDefault()
    if (this.busy) return
    const errors: Record<string, string> = {}
    const name = this.name.trim()
    if (!name) errors.name = 'A name is required.'
    else if (!NAME_RE.test(name)) errors.name = 'Lowercase letters, digits and dashes only.'
    else if (this.store.agents.data.some((a) => a.metadata.name === name)) errors.name = 'An agent with that name already exists.'
    if (!this.modelCredential) errors.modelCredential = 'Pick the model this agent reasons with.'
    this.errors = errors
    if (Object.keys(errors).length) return

    let body: AgentCreate = { name, displayName: name, modelCredential: this.modelCredential }
    const prompt = this.systemPrompt.trim()
    if (prompt) body.systemPrompt = prompt
    if (this.channel) body.channels = [{ name: 'primary', connectionRef: this.channel, primary: true }]
    const fams = ['core']
    if (this.web) fams.push('web')
    if (this.fanOut) fams.push('spawn')
    if (fams.length > 1) body.interactiveFamilies = fams
    if (!this.routeOwned) {
      this.dispatchEvent(new CustomEvent<AgentCreate>('agents-create', { detail: body }))
      return
    }

    this.busy = true
    const res = await mutate(this.store, {
      run: () => this.api.createAgent(body),
      success: `Agent “${name}” created.`,
      failure: 'Create failed',
      reload: ['agents'],
    })
    this.busy = false
    if (!res) return
    this.dispatchEvent(
      new CustomEvent<CreateSuccessDetail>('agents-create-success', {
        detail: { resource: 'agent', name, item: res },
        bubbles: true,
        composed: true,
      }),
    )
  }

  private cancel(): void {
    if (this.routeOwned) {
      this.dispatchEvent(new CustomEvent('agents-cancel', { bubbles: true, composed: true }))
      return
    }
    this.dispatchEvent(new CustomEvent('agents-cancel'))
  }

  render(): TemplateResult {
    const creds = this.store.credentials.data
    const channels = this.store.channelConnections()
    const form = html`<form
      class=${this.routeOwned ? 'agents-create-form k-create-surface' : 'agents-dialog'}
      role=${this.routeOwned ? nothing : 'dialog'}
      aria-modal=${this.routeOwned ? nothing : 'true'}
      aria-label="Create agent"
      @submit=${(e: Event) => void this.submit(e)}
    >
        ${this.routeOwned ? nothing : html`<header class="agents-dialog-head">
          <span class="agents-dialog-ic">${icon('bot')}</span>
          <h3>New agent</h3>
        </header>`}

        <div class=${this.routeOwned ? 'k-create-body' : ''}>
        <label>
          Name *
          <input class="k-input"
            name="name"
            .value=${this.name}
            placeholder="research-bot"
            autocomplete="off"
            aria-invalid=${this.errors.name ? 'true' : nothing}
            @input=${(e: Event) => (this.name = (e.target as HTMLInputElement).value)}
          />
          ${this.errors.name
            ? html`<span class="agents-fielderr">${this.errors.name}</span>`
            : html`<span class="agents-hint">A short id you'll reference from schedules and triggers.</span>`}
        </label>

        <label>
          Model credential *
          <select class="k-input"
            .value=${this.modelCredential}
            aria-invalid=${this.errors.modelCredential ? 'true' : nothing}
            @change=${(e: Event) => (this.modelCredential = (e.target as HTMLSelectElement).value)}
          >
            <option value="">— pick a model —</option>
            ${creds.map((c) => html`<option value=${c.name} ?selected=${c.name === this.modelCredential}>${c.name}${c.model ? ` (${c.model})` : ''}</option>`)}
          </select>
          ${this.errors.modelCredential ? html`<span class="agents-fielderr">${this.errors.modelCredential}</span>` : nothing}
          ${creds.length === 0
            ? html`<span class="agents-hint"
                >No model credentials yet —
                <button type="button" class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'menu', menu: 'models' })}>
                  add one under Models
                </button>
                first.</span
              >`
            : nothing}
        </label>

        <label>
          System prompt
          <span class="agents-hint">optional — persona and standing instructions, not mechanics</span>
          <textarea class="k-input"
            rows="3"
            placeholder="You are a concise assistant that…"
            .value=${this.systemPrompt}
            @input=${(e: Event) => (this.systemPrompt = (e.target as HTMLTextAreaElement).value)}
          ></textarea>
        </label>

        <label>
          Primary channel <span class="agents-hint">optional — where this agent messages you</span>
          <select class="k-input" @change=${(e: Event) => (this.channel = (e.target as HTMLSelectElement).value)}>
            <option value="">— none —</option>
            ${channels.map((c) => html`<option value=${c.metadata.name} ?selected=${c.metadata.name === this.channel}>${c.spec.displayName || c.metadata.name} (${c.spec.type})</option>`)}
          </select>
        </label>

        <fieldset class="agents-cap-fs">
          <legend>Can do <span class="agents-hint">— changeable later</span></legend>
          <label class="agents-cap">
            <input type="checkbox" .checked=${this.web} @change=${(e: Event) => (this.web = (e.target as HTMLInputElement).checked)} />
            <span><strong>Read the web</strong> <span class="muted">— fetch pages; search needs a websearch tool</span></span>
          </label>
          <label class="agents-cap">
            <input
              type="checkbox"
              .checked=${this.fanOut}
              @change=${(e: Event) => (this.fanOut = (e.target as HTMLInputElement).checked)}
            />
            <span><strong>Research fan-out</strong> <span class="muted">— work independent parts in parallel</span></span>
          </label>
        </fieldset>
        </div>

        <div class=${this.routeOwned ? 'k-create-actions' : 'agents-form-actions'}>
          <button type="button" class="k-btn k-btn--ghost secondary" @click=${() => this.cancel()}>Cancel</button>
          <button class="k-btn k-btn--primary" type="submit">${icon('check')} Create agent</button>
        </div>
      </form>`
    return this.routeOwned
      ? html`<div class="agents-create-page k-create-page">
          <button type="button" class="k-btn k-btn--ghost k-back-action" @click=${() => this.cancel()}>${icon('arrow-left')} Agents</button>
          <header class="k-create-header"><h1 class="k-create-title">Create agent</h1><p class="k-create-description">Choose the model, instructions, and optional channel this agent starts with.</p></header>
          ${form}
        </div>`
      : html`<div
          class="agents-overlay"
          @click=${(e: Event) => e.target === e.currentTarget && this.cancel()}
          @keydown=${(e: KeyboardEvent) => e.key === 'Escape' && this.cancel()}
        >${form}</div>`
  }
}

if (!customElements.get('agents-agent-create')) customElements.define('agents-agent-create', AgentCreateWizard)
