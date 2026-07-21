// Inbox menu: approvals and questions agents raise across the workspace.
// Approve/deny grants one tool call.

import { ic } from '../icons'
import type { ViewCtx } from '../view'
import { escapeHTML } from '../types'
import { resolveInbox } from '../actions'

export function render(vc: ViewCtx): string {
  const items = vc.store.inbox
  return `
    <div class="agents-panel">
      <h3>Inbox</h3>
      <p class="muted">Approvals and questions your agents raise, across the workspace (also delivered to each agent's channel). Approve/deny grants one tool call.</p>
      <table class="agents-table">
        <thead><tr><th>Agent</th><th>Kind</th><th>Prompt</th><th>State</th><th class="agents-th-actions"></th></tr></thead>
        <tbody>
          ${
            items.length
              ? items
                  .map(
                    (i) => `<tr>
                      <td>${escapeHTML(i.agentName)}</td>
                      <td>${escapeHTML(i.kind)}</td>
                      <td>${escapeHTML(i.prompt)}</td>
                      <td>${escapeHTML(i.state)}</td>
                      <td class="agents-row-actions">${i.state === 'pending' ? `<button data-approve="${escapeHTML(i.id)}">Approve</button><button class="secondary" data-deny="${escapeHTML(i.id)}">Deny</button>` : ''}</td>
                    </tr>`,
                  )
                  .join('')
              : `<tr class="agents-empty-row"><td colspan="5"><span class="agents-empty">${ic('inbox')} Nothing needs your attention.</span></td></tr>`
          }
        </tbody>
      </table>
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  root.querySelectorAll<HTMLElement>('[data-approve]').forEach((el) => el.addEventListener('click', () => void resolveInbox(vc, el.dataset.approve!, 'approve')))
  root.querySelectorAll<HTMLElement>('[data-deny]').forEach((el) => el.addEventListener('click', () => void resolveInbox(vc, el.dataset.deny!, 'deny')))
}
