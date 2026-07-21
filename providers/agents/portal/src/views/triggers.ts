// Triggers menu: a flat list of every trigger in the workspace, each bound to an
// agent via agentRef and fired by an external source (webhook / github). Same
// table + Agent-select form pattern as schedules.

import type { ViewCtx } from '../view'
import type { Trigger } from '../types'
import { escapeHTML, fmtTime } from '../types'
import { createTrigger, updateTrigger, deleteTrigger, runTrigger } from '../actions'

let editName: string | null = null
let creating = false

function agentOptions(vc: ViewCtx, selected: string): string {
  return vc.store.agents.map((a) => `<option value="${escapeHTML(a.metadata.name)}" ${a.metadata.name === selected ? 'selected' : ''}>${escapeHTML(a.spec?.displayName || a.metadata.name)}</option>`).join('')
}
function connOptions(vc: ViewCtx, selected: string): string {
  return [`<option value="">— none —</option>`, ...vc.store.connections.map((c) => `<option value="${escapeHTML(c.metadata.name)}" ${c.metadata.name === selected ? 'selected' : ''}>${escapeHTML(c.metadata.name)}</option>`)].join('')
}

function formHTML(vc: ViewCtx, editing: Trigger | undefined): string {
  const s = editing?.spec
  const source = s?.source || 'webhook'
  return `<form class="agents-obj-form" ${editing ? `data-edit="${escapeHTML(editing.metadata.name)}"` : ''}>
      <h4>${editing ? `Edit trigger <code>${escapeHTML(editing.metadata.name)}</code>` : 'New trigger'}</h4>
      ${editing ? '' : `<div class="agents-grid2"><label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="on-issue" /></label><label>Agent<select name="agentRef" required>${agentOptions(vc, '')}</select></label></div>`}
      <div class="agents-grid2">
        <label>Source<select name="source">${['webhook', 'github'].map((v) => `<option value="${v}" ${v === source ? 'selected' : ''}>${v}</option>`).join('')}</select></label>
        <label>Connection<select name="connectionRef">${connOptions(vc, s?.connectionRef || '')}</select></label>
      </div>
      <label>Task on fire<textarea name="task" rows="3" placeholder="Triage the incoming event.">${escapeHTML(s?.task || '')}</textarea></label>
      <label class="agents-check"><input type="checkbox" name="suspend" ${s?.suspend ? 'checked' : ''} /> Paused</label>
      <div class="agents-form-actions"><button>${editing ? 'Save' : 'Create trigger'}</button><button type="button" class="secondary" data-objcancel>Cancel</button></div>
    </form>`
}

export function render(vc: ViewCtx): string {
  const editing = editName ? vc.store.triggers.find((t) => t.metadata.name === editName) : undefined
  const showForm = creating || !!editing
  const rows =
    vc.store.triggers.length === 0
      ? `<tr class="agents-empty-row"><td colspan="6"><span class="agents-empty">⚡ No triggers yet — add one below.</span></td></tr>`
      : vc.store.triggers
          .map((t) => {
            const status = t.spec.suspend ? '<span class="agents-badge">paused</span>' : t.status?.lastFired ? `last ${escapeHTML(fmtTime(t.status.lastFired))}` : 'armed'
            return `<tr class="${editName === t.metadata.name ? 'is-editing' : ''}">
              <td><strong>${escapeHTML(t.metadata.name)}</strong></td>
              <td><button class="agents-linkbtn" data-goagent="${escapeHTML(t.spec.agentRef)}">${escapeHTML(t.spec.agentRef)}</button></td>
              <td class="mono">${escapeHTML(t.spec.source)}${t.spec.connectionRef ? ` <span class="muted">${escapeHTML(t.spec.connectionRef)}</span>` : ''}</td>
              <td class="muted">${status}</td>
              <td class="agents-row-actions">
                <button class="agents-iconbtn" data-runtrig="${escapeHTML(t.metadata.name)}" title="Fire now">▶</button>
                <button class="agents-iconbtn" data-edittrig="${escapeHTML(t.metadata.name)}" title="Edit">✏️</button>
                <button class="agents-iconbtn" data-susptrig="${escapeHTML(t.metadata.name)}" data-susp="${t.spec.suspend ? '1' : '0'}" title="${t.spec.suspend ? 'Resume' : 'Pause'}">${t.spec.suspend ? '▶️' : '⏸'}</button>
                <button class="agents-iconbtn agents-iconbtn-danger" data-deltrig="${escapeHTML(t.metadata.name)}" title="Delete">🗑</button>
              </td>
            </tr>`
          })
          .join('')
  return `
    <div class="agents-panel">
      <div class="agents-panel-head"><h3>Triggers</h3>${showForm ? '' : `<button data-newobj ${vc.store.agents.length ? '' : 'disabled title="Create an agent first"'}>＋ New trigger</button>`}</div>
      <p class="muted">External events that wake an agent. Each fires a specific agent. ${vc.store.agents.length ? '' : 'Create an agent first.'}</p>
      <table class="agents-table">
        <thead><tr><th>Name</th><th>Agent</th><th>Source</th><th>Status</th><th class="agents-th-actions">Actions</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
      ${showForm ? formHTML(vc, editing) : ''}
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  root.querySelector<HTMLElement>('[data-newobj]')?.addEventListener('click', () => {
    creating = true
    editName = null
    vc.rerender()
  })
  root.querySelectorAll<HTMLElement>('[data-goagent]').forEach((el) =>
    el.addEventListener('click', () => vc.navigate({ kind: 'agent', name: el.dataset.goagent!, tab: 'flow' })),
  )
  root.querySelectorAll<HTMLElement>('[data-runtrig]').forEach((el) => el.addEventListener('click', () => void runTrigger(vc, el.dataset.runtrig!)))
  root.querySelectorAll<HTMLElement>('[data-edittrig]').forEach((el) =>
    el.addEventListener('click', () => {
      editName = el.dataset.edittrig!
      creating = false
      vc.rerender()
    }),
  )
  root.querySelectorAll<HTMLElement>('[data-susptrig]').forEach((el) =>
    el.addEventListener('click', () => void updateTrigger(vc, el.dataset.susptrig!, { suspend: el.dataset.susp !== '1' }, el.dataset.susp === '1' ? 'Trigger resumed.' : 'Trigger paused.')),
  )
  root.querySelectorAll<HTMLElement>('[data-deltrig]').forEach((el) =>
    el.addEventListener('click', () => {
      if (confirm(`Delete trigger ${el.dataset.deltrig}?`)) void deleteTrigger(vc, el.dataset.deltrig!)
    }),
  )
  root.querySelector<HTMLElement>('[data-objcancel]')?.addEventListener('click', () => {
    creating = false
    editName = null
    vc.rerender()
  })
  const form = root.querySelector<HTMLFormElement>('.agents-obj-form')
  form?.addEventListener('submit', (e) => {
    e.preventDefault()
    const g = (n: string) => (form.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
    const suspend = !!form.querySelector<HTMLInputElement>('input[name=suspend]')?.checked
    const patch: Record<string, unknown> = { source: g('source') || 'webhook', connectionRef: g('connectionRef'), task: g('task'), suspend }
    const edit = form.dataset.edit
    if (edit) {
      void updateTrigger(vc, edit, patch)
      editName = null
    } else {
      const name = g('name')
      const agentRef = g('agentRef')
      if (!name || !agentRef) return
      void createTrigger(vc, { name, agentRef, ...patch }).then((ok) => {
        if (ok) creating = false
      })
    }
  })
}
