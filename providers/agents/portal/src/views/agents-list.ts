// Agents tab: the workspace's agents as a card grid. A card opens the agent's
// detail page (Config + live chat). The "New agent" tile opens the creation
// wizard, which — unlike the old bare name field — collects the model
// credential the agent needs to be usable on arrival.

import { html, nothing, type TemplateResult } from 'lit'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState, loadingState, sliceView, staleState } from '../ui/states'
import { confirmModal } from '../portalkit/modal'
import { mutate } from '../mutate'
import type { Agent } from '../types'
import { hashFor } from '../router'
import { firstRunGuide } from '../ui/create-flow'

export class AgentsList extends StoreElement {
  private async del(name: string): Promise<void> {
    const authority = this.captureAuthority()
    const ok = await confirmModal({
      title: `Delete agent “${name}”?`,
      message: 'This also deletes its chat history.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok || !this.authorityIsCurrent(authority)) return
    await mutate(authority.store, {
      run: () => authority.api.deleteAgent(name),
      success: `Agent “${name}” deleted.`,
      failure: 'Delete failed',
      optimistic: () => (authority.store.agents.data = authority.store.agents.data.filter((a) => a.metadata.name !== name)),
      reload: ['agents'],
    })
  }

  render(): TemplateResult {
    const agents = this.store.agents
    const credentials = this.store.credentials
    const showFirstRun = agents.loaded && agents.data.length === 0 && (!agents.error || agents.hasSnapshot)
    const renderFirstRun = (): TemplateResult => {
      const retryCredentials = () => void this.store.load('credentials')
      if (credentials.error && !credentials.hasSnapshot) {
        return errorState(`Could not load model credentials. ${credentials.error}`, retryCredentials)
      }
      if (!credentials.loaded) return loadingState('Loading model credentials…')

      const needsModel = credentials.data.length === 0
      const guide = firstRunGuide({
        icon: 'bot',
        title: needsModel ? 'Connect a model before creating your first agent' : 'Create your first agent',
        description: needsModel
          ? 'Agents need a model credential to reason. Add one first, then return here to choose instructions, tools, and a channel.'
          : 'Give an agent a model and standing instructions, then start a conversation or automate its work.',
        primaryLabel: needsModel ? 'Add model credential' : 'Create agent',
        primary: () => this.navigate({ kind: 'create', resource: needsModel ? 'model' : 'agent' }),
        steps: [
          { label: 'Model', description: 'Credential and model endpoint' },
          { label: 'Agent', description: 'Identity, instructions, and capabilities' },
          { label: 'Conversation', description: 'Chat directly or add automation' },
        ],
        currentStep: needsModel ? 0 : 1,
        journeyLabel: 'Agent setup path',
      })
      return credentials.error
        ? html`${staleState(credentials.error, retryCredentials)}${guide}`
        : guide
    }
    return html`
      <div class="agents-menu">
        <div class="agents-panel-head">
          <h3>Agents</h3>
          ${showFirstRun
            ? nothing
            : html`<button class="k-btn k-btn--primary" @click=${() => this.navigate({ kind: 'create', resource: 'agent' })}>${icon('plus')} New agent</button>`}
        </div>
        ${sliceView<Agent>({
          slice: agents,
          emptyIcon: 'bot',
          emptyText: 'No agents yet — create one to get started.',
          empty: renderFirstRun,
          retry: () => void this.store.load('agents'),
          content: (rows) => html`<div class="agents-grid">
            ${repeat(
              rows,
              (a) => a.metadata.name,
              (a) => this.card(a),
            )}
          </div>`,
        })}
      </div>
    `
  }

  private card(a: Agent): TemplateResult {
    const name = a.metadata.name
    const model = a.spec?.models?.chat
    const nsched = this.store.schedules.data.filter((s) => s.spec.agentRef === name).length
    const ntrig = this.store.triggers.data.filter((t) => t.spec.agentRef === name).length
    const chans = a.spec?.channels || []
    const primary = chans.find((ch) => ch.primary) || chans[0]
    const chan = primary ? primary.connectionRef + (chans.length > 1 ? ` +${chans.length - 1}` : '') : ''
    const agentRoute = hashFor({ kind: 'agent', name, tab: 'config' })
    return html`
      <article class="agents-card k-card">
        <a class="agents-card-link" href=${agentRoute} aria-label="Open agent ${a.spec?.displayName || name}">
          <div class="agents-card-glyph">${icon('bot')}</div>
          <div class="agents-card-body">
            <h3>${a.spec?.displayName || name}</h3>
            <p class="agents-card-model ${model ? '' : 'warn'}">${model || 'no model — pick one in Config'}</p>
          </div>
          <div class="agents-card-foot">
            <span>${nsched} schedule${nsched === 1 ? '' : 's'}</span>
            <span>${ntrig} trigger${ntrig === 1 ? '' : 's'}</span>
            <span>${chan ? html`${icon('megaphone')} ${chan}` : 'no channel'}</span>
          </div>
        </a>
        <div class="agents-card-actions">
          <button
            class="k-btn k-btn--ghost agents-card-chat"
            @click=${(e: Event) => {
              e.stopPropagation()
              this.navigate({ kind: 'agent', name, tab: 'config' })
            }}
          >
            ${icon('message')} Open
          </button>
          <button
            class="k-btn k-btn--ghost secondary"
            @click=${(e: Event) => {
              e.stopPropagation()
              this.navigate({ kind: 'agent', name, tab: 'runs' })
            }}
          >
            ${icon('gauge')} Runs
          </button>
          <button
            class="k-icon-action agents-iconbtn-danger"
            aria-label="Delete agent ${name}"
            title="Delete agent"
            @click=${(e: Event) => {
              e.stopPropagation()
              void this.del(name)
            }}
          >
            ${icon('trash')}
          </button>
        </div>
      </article>
    `
  }
}

if (!customElements.get('agents-agents-list')) customElements.define('agents-agents-list', AgentsList)
