// Agent detail page: a header (back to Agents, title, Chat|Flow|Settings tabs,
// delete) over one of three tab bodies. Chat is the default. The flow tab mounts
// the imperative FlowCanvas after render.

import { confirmModal } from '../portalkit/modal'
import { ic } from '../portalkit/icons'
import type { ViewCtx } from '../view'
import type { AgentTab } from '../router'
import { escapeHTML } from '../types'
import { deleteAgent } from '../actions'
import * as chat from './agent-chat'
import * as settings from './agent-settings'
import * as flowView from './flow-view'

const TABS: [AgentTab, string][] = [
  ['chat', `${ic('message')} Chat`],
  ['flow', `${ic('workflow')} Flow`],
  ['settings', `${ic('settings')} Settings`],
]

export function render(vc: ViewCtx, name: string, tab: AgentTab): string {
  const a = vc.store.agent(name)
  if (!a) {
    // Agents may still be loading — show a gentle placeholder rather than an
    // error, since the list refreshes into place.
    return `<div class="agents-detail"><div class="agents-detail-head"><div class="agents-detail-title"><button class="agents-back" data-back>${ic('arrow-left')} Agents</button></div></div><div class="agents-empty"><p class="muted">Loading agent…</p></div></div>`
  }
  const body = tab === 'chat' ? chat.render(a) : tab === 'settings' ? settings.render(vc, a) : `<div class="agents-flow-host" data-flow-host></div>`
  return `
    <div class="agents-detail ${tab === 'flow' ? 'is-flow' : ''}">
      <div class="agents-detail-head">
        <div class="agents-detail-title">
          <button class="agents-back" data-back>${ic('arrow-left')} Agents</button>
          <h2>${escapeHTML(a.spec?.displayName || a.metadata.name)}</h2>
        </div>
        <div class="agents-detail-actions">
          <nav class="agents-subnav">
            ${TABS.map(([id, label]) => `<button class="agents-subtab ${tab === id ? 'sel' : ''}" data-subtab="${id}">${label}</button>`).join('')}
          </nav>
          <button class="secondary" data-delagent="${escapeHTML(a.metadata.name)}">Delete</button>
        </div>
      </div>
      ${tab === 'flow' ? body : `<div class="agents-detail-body">${body}</div>`}
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement, name: string, tab: AgentTab): void {
  root.querySelector<HTMLElement>('[data-back]')?.addEventListener('click', () => vc.navigate({ kind: 'menu', menu: 'agents' }))
  root.querySelectorAll<HTMLElement>('[data-subtab]').forEach((el) =>
    el.addEventListener('click', () => vc.navigate({ kind: 'agent', name, tab: el.dataset.subtab as AgentTab })),
  )
  root.querySelector<HTMLElement>('[data-delagent]')?.addEventListener('click', async () => {
    if (await confirmModal({ title: `Delete agent “${name}”?`, message: 'This also deletes its chat history.', danger: true, confirmLabel: 'Delete' })) {
      void deleteAgent(vc, name).then(() => vc.navigate({ kind: 'menu', menu: 'agents' }))
    }
  })
  const a = vc.store.agent(name)
  if (!a) return
  if (tab === 'chat') chat.wire(vc, root, a)
  else if (tab === 'settings') settings.wire(vc, root, a)
  else flowView.mount(vc, root, name)
}
