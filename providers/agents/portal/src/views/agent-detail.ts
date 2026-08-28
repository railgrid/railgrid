// Agent detail page — the one canonical place an agent is edited.
//
// Config tab = two panes: the full configuration form on the left, a live chat
// playground on the right (edit config, try it immediately). Runs tab = this
// agent's Activity feed, filtered.

import { html, nothing, type TemplateResult } from 'lit'
import { property } from 'lit/decorators.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { loadingState } from '../ui/states'
import { confirmModal } from '../portalkit/modal'
import { tabClass, tabsClass } from '../portalkit/tabs'
import { mutate } from '../mutate'
import type { AgentTab } from '../router'

import './agent-config'
import './agent-chat'
import './activity'

const TABS: [AgentTab, TemplateResult][] = [
  ['config', html`${icon('sliders')} Config`],
  ['runs', html`${icon('gauge')} Runs`],
]

export class AgentDetail extends StoreElement {
  @property({ type: String }) name = ''
  @property({ type: String }) tab: AgentTab = 'config'

  private async del(): Promise<void> {
    const authority = this.captureAuthority()
    const name = this.name
    const ok = await confirmModal({
      title: `Delete agent “${name}”?`,
      message: 'This also deletes its chat history.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok || !this.authorityIsCurrent(authority)) return
    const res = await mutate(authority.store, {
      run: () => authority.api.deleteAgent(name),
      success: `Agent “${name}” deleted.`,
      failure: 'Delete failed',
      reload: ['agents'],
    })
    if (res !== undefined && this.authorityIsCurrent(authority)) this.navigate({ kind: 'menu', menu: 'agents' })
  }

  render(): TemplateResult {
    const a = this.store.agent(this.name)
    const back = html`<button class="k-btn k-btn--ghost agents-back" @click=${() => this.navigate({ kind: 'menu', menu: 'agents' })}>
      ${icon('arrow-left')} Agents
    </button>`
    if (!a) {
      // Agents may still be loading — a placeholder beats an error, since the
      // list refreshes into place.
      return html`<div class="agents-detail">
        <div class="agents-detail-head"><div class="agents-detail-title">${back}</div></div>
        ${this.store.agents.loaded
          ? html`<div class="k-card agents-state agents-state-empty" role="status">${icon('bot')} No agent named “${this.name}” in this workspace.</div>`
          : loadingState('Loading agent…')}
      </div>`
    }
    return html`
      <div class="agents-detail">
        <div class="agents-detail-head">
          <div class="agents-detail-title">
            ${back}
            <h2>${a.spec?.displayName || a.metadata.name}</h2>
            ${a.status?.suspendedReason ? html`<span class="k-badge agents-badge k-badge--warning agents-badge-warn">${a.status.suspendedReason}</span>` : nothing}
          </div>
          <div class="agents-detail-actions">
            <nav class="${tabsClass('agents-subnav')}" aria-label="Agent sections">
              ${TABS.map(
                ([id, label]) => html`<button
                  class="k-btn k-btn--ghost ${tabClass({ active: this.tab === id, className: 'agents-subtab' })}"
                  type="button"
                  aria-current=${this.tab === id ? 'page' : nothing}
                  @click=${() => this.navigate({ kind: 'agent', name: this.name, tab: id })}
                >
                  ${label}
                </button>`,
              )}
            </nav>
            <button class="k-btn k-btn--ghost secondary" @click=${() => void this.del()}>${icon('trash')} Delete</button>
          </div>
        </div>
        ${this.tab === 'runs'
          ? html`<agents-activity .store=${this.store} .api=${this.api} .agent=${this.name}></agents-activity>`
          : html`<div class="agents-split">
              <div class="agents-split-config">
                <agents-agent-config .store=${this.store} .api=${this.api} .name=${this.name}></agents-agent-config>
              </div>
              <div class="agents-split-chat">
                <agents-agent-chat .store=${this.store} .api=${this.api} .name=${this.name}></agents-agent-chat>
              </div>
            </div>`}
      </div>
    `
  }
}

if (!customElements.get('agents-agent-detail')) customElements.define('agents-agent-detail', AgentDetail)
