// Agents menu: the workspace's agents as a card grid. Clicking a card opens the
// agent's detail page (Chat first). Each card has quick Chat / Flow actions and
// a delete. A dashed "new agent" tile creates one and jumps into it.

import type { ViewCtx } from '../view'
import { escapeHTML } from '../types'
import { createAgent, deleteAgent } from '../actions'

export function render(vc: ViewCtx): string {
  const count = (agent: string, arr: { spec: { agentRef: string } }[]) => arr.filter((x) => x.spec.agentRef === agent).length
  const cards = vc.store.agents
    .map((a) => {
      const model = a.spec?.models?.chat
      const nsched = count(a.metadata.name, vc.store.schedules)
      const ntrig = count(a.metadata.name, vc.store.triggers)
      const chan = a.spec?.defaultNotifyConnection
      return `
        <article class="agents-card" data-agent="${escapeHTML(a.metadata.name)}">
          <div class="agents-card-glyph">🤖</div>
          <div class="agents-card-body">
            <h3>${escapeHTML(a.spec?.displayName || a.metadata.name)}</h3>
            <p class="agents-card-model ${model ? '' : 'warn'}">${model ? escapeHTML(model) : 'no model — set up in Settings'}</p>
          </div>
          <div class="agents-card-foot">
            <span>${nsched} schedule${nsched === 1 ? '' : 's'}</span>
            <span>${ntrig} trigger${ntrig === 1 ? '' : 's'}</span>
            <span>${chan ? '📣 ' + escapeHTML(chan) : 'no channel'}</span>
          </div>
          <div class="agents-card-actions">
            <button class="agents-card-chat" data-chat="${escapeHTML(a.metadata.name)}">💬 Chat</button>
            <button class="secondary agents-card-flow" data-flow="${escapeHTML(a.metadata.name)}" title="Open flow">◆ Flow</button>
            <button class="agents-iconbtn agents-iconbtn-danger" data-delagent="${escapeHTML(a.metadata.name)}" title="Delete agent">🗑</button>
          </div>
        </article>`
    })
    .join('')

  return `
    <div class="agents-menu">
      <div class="agents-grid">
        <form class="agents-card agents-card-new">
          <div class="agents-card-glyph">＋</div>
          <input name="name" placeholder="new-agent-id" required pattern="[a-z0-9-]+" />
          <button>Create agent</button>
        </form>
        ${cards}
      </div>
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  const open = (name: string, tab: 'chat' | 'flow') => vc.navigate({ kind: 'agent', name, tab })
  root.querySelectorAll<HTMLElement>('.agents-card[data-agent]').forEach((el) => el.addEventListener('click', () => open(el.dataset.agent!, 'chat')))
  root.querySelectorAll<HTMLButtonElement>('[data-chat]').forEach((el) =>
    el.addEventListener('click', (e) => {
      e.stopPropagation()
      open(el.dataset.chat!, 'chat')
    }),
  )
  root.querySelectorAll<HTMLButtonElement>('[data-flow]').forEach((el) =>
    el.addEventListener('click', (e) => {
      e.stopPropagation()
      open(el.dataset.flow!, 'flow')
    }),
  )
  root.querySelectorAll<HTMLButtonElement>('[data-delagent]').forEach((el) =>
    el.addEventListener('click', (e) => {
      e.stopPropagation()
      const name = el.dataset.delagent!
      if (confirm(`Delete agent ${name} and its history?`)) void deleteAgent(vc, name)
    }),
  )
  const nf = root.querySelector<HTMLFormElement>('.agents-card-new')
  nf?.addEventListener('submit', (e) => {
    e.preventDefault()
    const v = nf.querySelector<HTMLInputElement>('input')!.value.trim()
    if (v) void createAgent(vc, v).then((ok) => ok && vc.navigate({ kind: 'agent', name: v, tab: 'chat' }))
  })
}
