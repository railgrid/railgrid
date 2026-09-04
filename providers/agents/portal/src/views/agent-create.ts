// Agent creation wizard. The old create form was a bare name field, so every
// new agent landed in chat saying "no model assigned" — the one thing it needs
// to do anything. The model credential is required here; the system prompt and
// a primary channel are optional but offered because they are the next two
// things everyone sets.

import { html, nothing, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState, loadingState, staleState } from '../ui/states'
import { mutate } from '../mutate'
import type { CreateSuccessDetail } from '../router'
import type { Agent, AgentCreate } from '../types'
import { createGuidance } from '../ui/create-flow'

const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/

export class AgentCreateWizard extends StoreElement {
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
  private focusedName = false

  protected updated(): void {
    if (this.focusedName) return
    const input = this.querySelector<HTMLInputElement>('input[name=name]')
    if (!input) return
    input.focus()
    this.focusedName = true
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
    this.busy = true
    let res: Agent | undefined
    try {
      res = await mutate(this.store, {
        run: () => this.api.createAgent(body),
        success: `Agent “${name}” created.`,
        failure: 'Create failed',
        reload: ['agents'],
      })
    } finally {
      this.busy = false
    }
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
    this.dispatchEvent(new CustomEvent('agents-cancel', { bubbles: true, composed: true }))
  }

  render(): TemplateResult {
    const agents = this.store.agents
    const credentials = this.store.credentials
    const retryAgents = () => void this.store.load('agents')
    const retryCredentials = () => void this.store.load('credentials')
    let prerequisiteState: TemplateResult | null = null
    if (agents.error && !agents.hasSnapshot) {
      prerequisiteState = errorState(`Could not load existing agents. ${agents.error}`, retryAgents)
    } else if (credentials.error && !credentials.hasSnapshot) {
      prerequisiteState = errorState(`Could not load model credentials. ${credentials.error}`, retryCredentials)
    } else if (!agents.hasSnapshot || !credentials.hasSnapshot) {
      prerequisiteState = loadingState('Loading existing agents and model credentials…')
    }

    if (prerequisiteState) {
      return html`<div class="agents-create-page k-create-page">
        <button type="button" class="k-btn k-btn--ghost k-back-action" @click=${() => this.cancel()}>${icon('arrow-left')} Agents</button>
        <header class="k-create-header"><h1 class="k-create-title">Create agent</h1><p class="k-create-description">Choose the model, instructions, and optional channel this agent starts with.</p></header>
        ${prerequisiteState}
      </div>`
    }

    const creds = credentials.data
    const channels = this.store.channelConnections()
    const staleNotices = html`
      ${agents.error ? staleState(agents.error, retryAgents) : nothing}
      ${credentials.error ? staleState(credentials.error, retryCredentials) : nothing}
    `
    const form = html`<form
      class="agents-create-form agents-guided-form k-create-surface k-create-surface--guided"
      aria-label="Create agent"
      aria-busy=${this.busy ? 'true' : 'false'}
      @submit=${(e: Event) => void this.submit(e)}
    >
        <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
        <label for="agent-create-name">
          Name *
          <input id="agent-create-name" class="k-input"
            name="name"
            .value=${this.name}
            placeholder="research-bot"
            autocomplete="off"
            required
            ?disabled=${this.busy}
            aria-invalid=${this.errors.name ? 'true' : nothing}
            aria-describedby=${this.errors.name ? 'agent-create-name-hint agent-create-name-error' : 'agent-create-name-hint'}
            @input=${(e: Event) => (this.name = (e.target as HTMLInputElement).value)}
          />
          ${this.errors.name
            ? html`<span id="agent-create-name-error" class="agents-fielderr" role="alert">${this.errors.name}</span>`
            : nothing}
          <span id="agent-create-name-hint" class="agents-hint">A short id you'll reference from schedules and triggers.</span>
        </label>

        <label for="agent-create-model">
          Model credential *
          <select id="agent-create-model" class="k-input"
            name="modelCredential"
            .value=${this.modelCredential}
            required
            ?disabled=${this.busy}
            aria-invalid=${this.errors.modelCredential ? 'true' : nothing}
            aria-describedby=${this.errors.modelCredential ? 'agent-create-model-hint agent-create-model-error' : 'agent-create-model-hint'}
            @change=${(e: Event) => (this.modelCredential = (e.target as HTMLSelectElement).value)}
          >
            <option value="">— pick a model —</option>
            ${creds.map((c) => html`<option value=${c.name} ?selected=${c.name === this.modelCredential}>${c.name}${c.model ? ` (${c.model})` : ''}</option>`)}
          </select>
          ${this.errors.modelCredential ? html`<span id="agent-create-model-error" class="agents-fielderr" role="alert">${this.errors.modelCredential}</span>` : nothing}
          <span id="agent-create-model-hint" class="agents-hint">The credential and model endpoint used for every turn.</span>
          ${creds.length === 0
            ? html`<span class="agents-hint"
                >No model credentials yet —
                <button type="button" class="k-btn k-btn--ghost agents-linkbtn" ?disabled=${this.busy} @click=${() => this.navigate({ kind: 'create', resource: 'model' })}>
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
            ?disabled=${this.busy}
            @input=${(e: Event) => (this.systemPrompt = (e.target as HTMLTextAreaElement).value)}
          ></textarea>
        </label>

        <label>
          Primary channel <span class="agents-hint">optional — where this agent messages you</span>
          <select class="k-input" ?disabled=${this.busy} @change=${(e: Event) => (this.channel = (e.target as HTMLSelectElement).value)}>
            <option value="">— none —</option>
            ${channels.map((c) => html`<option value=${c.metadata.name} ?selected=${c.metadata.name === this.channel}>${c.spec.displayName || c.metadata.name} (${c.spec.type})</option>`)}
          </select>
        </label>

        <fieldset class="agents-cap-fs">
          <legend>Can do <span class="agents-hint">— changeable later</span></legend>
          <label class="agents-cap">
            <input type="checkbox" .checked=${this.web} ?disabled=${this.busy} @change=${(e: Event) => (this.web = (e.target as HTMLInputElement).checked)} />
            <span><strong>Read the web</strong> <span class="muted">— fetch pages; search needs a websearch tool</span></span>
          </label>
          <label class="agents-cap">
            <input
              type="checkbox"
              .checked=${this.fanOut}
              ?disabled=${this.busy}
              @change=${(e: Event) => (this.fanOut = (e.target as HTMLInputElement).checked)}
            />
            <span><strong>Research fan-out</strong> <span class="muted">— work independent parts in parallel</span></span>
          </label>
        </fieldset>
        </div>
        ${createGuidance({
          icon: 'bot',
          title: 'Prepare a usable agent',
          description: 'Choose the identity Faros will create and the model it can use immediately.',
          prerequisites: [
            creds.length ? 'A model credential is available in this workspace.' : 'Add a model credential before creating the agent.',
            'Optional channel connections can be added now or attached later from Config.',
          ],
          values: [
            { label: 'Agent name', value: this.name.trim() || 'Not entered yet', technical: true },
            { label: 'Model', value: this.modelCredential || 'Not selected', technical: true },
            { label: 'Primary channel', value: this.channel || 'None', technical: true },
            { label: 'Capabilities', value: [this.web ? 'web' : '', this.fanOut ? 'fan-out' : ''].filter(Boolean).join(', ') || 'Core only' },
          ],
          nextSteps: [
            'Faros creates the agent and opens its Config workspace.',
            'Start a conversation to verify the model and instructions.',
            'Attach toolsets, schedules, and triggers when the core behavior is ready.',
          ],
        })}
        </div>

        <div class="k-create-actions">
          <button type="button" class="k-btn k-btn--ghost secondary" ?disabled=${this.busy} @click=${() => this.cancel()}>Cancel</button>
          <button class="k-btn k-btn--primary" type="submit" ?disabled=${this.busy || creds.length === 0}>${icon('check')} ${this.busy ? 'Creating…' : 'Create agent'}</button>
        </div>
      </form>`
    return html`<div class="agents-create-page k-create-page">
      <button type="button" class="k-btn k-btn--ghost k-back-action" ?disabled=${this.busy} @click=${() => this.cancel()}>${icon('arrow-left')} Agents</button>
      <header class="k-create-header"><h1 class="k-create-title">Create agent</h1><p class="k-create-description">Choose the model, instructions, and optional channel this agent starts with.</p></header>
      ${staleNotices}
      ${form}
    </div>`
  }
}

if (!customElements.get('agents-agent-create')) customElements.define('agents-agent-create', AgentCreateWizard)
