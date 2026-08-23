// Agents tab: the workspace's agents as a card grid. A card opens the agent's
// detail page (Config + live chat). The "New agent" tile opens the creation
// wizard, which — unlike the old bare name field — collects the model
// credential the agent needs to be usable on arrival.

import { html, nothing, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { sliceView } from '../ui/states'
import { confirmModal } from '../portalkit/modal'
import { mutate } from '../mutate'
import type { Agent, AgentCreate } from '../types'

import './agent-create'

export class AgentsList extends StoreElement {
  @state() private creating = false

  private async del(name: string): Promise<void> {
    const ok = await confirmModal({
      title: `Delete agent “${name}”?`,
      message: 'This also deletes its chat history.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok) return
    await mutate(this.store, {
      run: () => this.api.deleteAgent(name),
      success: `Agent “${name}” deleted.`,
      failure: 'Delete failed',
      optimistic: () => (this.store.agents.data = this.store.agents.data.filter((a) => a.metadata.name !== name)),
      reload: ['agents'],
    })
  }

  private async create(body: AgentCreate): Promise<void> {
    const res = await mutate(this.store, {
      run: () => this.api.createAgent(body),
      success: `Agent “${body.name}” created.`,
      failure: 'Create failed',
      reload: ['agents'],
    })
    if (!res) return
    this.creating = false
    this.navigate({ kind: 'agent', name: body.name, tab: 'config' })
  }

  render(): TemplateResult {
    return html`
      <div class="agents-menu">
        <div class="agents-panel-head">
          <h3>Agents</h3>
          <button class="k-btn k-btn--primary" @click=${() => (this.creating = true)}>${icon('plus')} New agent</button>
        </div>
        ${sliceView<Agent>({
          slice: this.store.agents,
          emptyIcon: 'bot',
          emptyText: 'No agents yet — create one to get started.',
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
      ${this.creating
        ? html`<agents-agent-create
            .store=${this.store}
            .api=${this.api}
            @agents-create=${(e: CustomEvent<AgentCreate>) => void this.create(e.detail)}
            @agents-cancel=${() => (this.creating = false)}
          ></agents-agent-create>`
        : nothing}
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
    const open = (): void => this.navigate({ kind: 'agent', name, tab: 'config' })
    const isNestedControl = (event: KeyboardEvent): boolean => {
      const currentTarget = event.currentTarget as Element | null
      const target = event.target as Element | null
      const control = target?.closest?.(
        'a, button, input, select, textarea, summary, [contenteditable="true"], [role="button"], [role="link"]',
      )
      return Boolean(control && control !== currentTarget)
    }
    return html`
      <article
        class="agents-card k-card"
        tabindex="0"
        role="link"
        aria-label="Open agent ${a.spec?.displayName || name}"
        @click=${open}
        @keydown=${(e: KeyboardEvent) => {
          if (isNestedControl(e)) return
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            open()
          }
        }}
      >
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
        <div class="agents-card-actions">
          <button
            class="k-btn k-btn--ghost agents-card-chat"
            @click=${(e: Event) => {
              e.stopPropagation()
              open()
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
            class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger"
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
