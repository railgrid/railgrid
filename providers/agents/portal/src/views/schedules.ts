// Schedules menu: a flat list of every schedule in the workspace, each bound to
// an agent via agentRef. Create/edit with an Agent select (required — the
// backend rejects an empty agentRef), cron/timezone or one-shot runAt, task,
// suspend. Run-now, edit, suspend toggle, delete per row.

import { confirmModal } from '../portalkit/modal'
import { ic } from '../portalkit/icons'
import type { ViewCtx } from '../view'
import type { Schedule } from '../types'
import { escapeHTML, fmtTime } from '../types'
import { createSchedule, updateSchedule, deleteSchedule, runSchedule } from '../actions'

let editName: string | null = null // null = no form open; '' = create; name = edit
let creating = false

function agentOptions(vc: ViewCtx, selected: string): string {
  return vc.store.agents.map((a) => `<option value="${escapeHTML(a.metadata.name)}" ${a.metadata.name === selected ? 'selected' : ''}>${escapeHTML(a.spec?.displayName || a.metadata.name)}</option>`).join('')
}

function formHTML(vc: ViewCtx, editing: Schedule | undefined, agentRef?: string): string {
  const s = editing?.spec
  const type = s?.type || 'cron'
  // When scoped to one agent (the Wiring tab), the agent is fixed — bind it in
  // wire() and collect only the name.
  const nameRow = agentRef
    ? `<label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="daily-digest" /></label>`
    : `<div class="agents-grid2"><label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="daily-digest" /></label><label>Agent<select name="agentRef" required>${agentOptions(vc, '')}</select></label></div>`
  return `<form class="agents-obj-form" ${editing ? `data-edit="${escapeHTML(editing.metadata.name)}"` : ''}>
      <h4>${editing ? `Edit schedule <code>${escapeHTML(editing.metadata.name)}</code>` : 'New schedule'}</h4>
      ${editing ? '' : nameRow}
      <div class="agents-grid2">
        <label>Type<select name="type">${['cron', 'wakeup'].map((t) => `<option value="${t}" ${t === type ? 'selected' : ''}>${t === 'wakeup' ? 'one-shot (runAt)' : 'recurring (cron)'}</option>`).join('')}</select></label>
        <label>Timezone<input name="timeZone" value="${escapeHTML(s?.timeZone || '')}" placeholder="Europe/Vilnius" /></label>
      </div>
      <label class="agents-when-cron">Cron<input name="schedule" class="mono" value="${escapeHTML(s?.schedule || '')}" placeholder="0 9 * * *" /><span class="agents-hint">5-field cron · crontab.guru</span></label>
      <label class="agents-when-wakeup">Run at (RFC3339)<input name="runAt" class="mono" value="${escapeHTML(s?.runAt || '')}" placeholder="2026-01-01T09:00:00Z" /></label>
      <label>Task<textarea name="task" rows="3" placeholder="Summarise today's open PRs and post to my channel.">${escapeHTML(s?.task || s?.checklist || '')}</textarea></label>
      <label class="agents-check"><input type="checkbox" name="suspend" ${s?.suspend ? 'checked' : ''} /> Paused</label>
      <div class="agents-form-actions"><button>${editing ? 'Save' : 'Create schedule'}</button><button type="button" class="secondary" data-objcancel>Cancel</button></div>
    </form>`
}

export function render(vc: ViewCtx, agentRef?: string): string {
  const scoped = !!agentRef
  const list = scoped ? vc.store.schedules.filter((s) => s.spec.agentRef === agentRef) : vc.store.schedules
  const editing = editName ? list.find((s) => s.metadata.name === editName) : undefined
  const showForm = creating || !!editing
  const cols = scoped ? 4 : 5
  const rows =
    list.length === 0
      ? `<tr class="agents-empty-row"><td colspan="${cols}"><span class="agents-empty">${ic('clock')} No schedules yet — add one below.</span></td></tr>`
      : list
          .map((s) => {
            const when = s.spec.type === 'wakeup' ? s.spec.runAt || '' : s.spec.schedule || ''
            const dis = s.status?.disabledReason
            const status = dis ? `<span class="agents-badge">${escapeHTML(dis)}</span>` : s.spec.suspend ? '<span class="agents-badge">paused</span>' : s.status?.nextRun ? `next ${escapeHTML(fmtTime(s.status.nextRun))}` : 'armed'
            return `<tr class="${editName === s.metadata.name ? 'is-editing' : ''}">
              <td><strong>${escapeHTML(s.metadata.name)}</strong></td>
              ${scoped ? '' : `<td><button class="agents-linkbtn" data-goagent="${escapeHTML(s.spec.agentRef)}">${escapeHTML(s.spec.agentRef)}</button></td>`}
              <td class="mono">${escapeHTML(when)}${s.spec.timeZone ? ` <span class="muted">${escapeHTML(s.spec.timeZone)}</span>` : ''}</td>
              <td class="muted">${status}</td>
              <td class="agents-row-actions">
                <button class="agents-iconbtn" data-runsched="${escapeHTML(s.metadata.name)}" title="Run now">${ic('play')}</button>
                <button class="agents-iconbtn" data-editsched="${escapeHTML(s.metadata.name)}" title="Edit">${ic('pencil')}</button>
                <button class="agents-iconbtn" data-suspsched="${escapeHTML(s.metadata.name)}" data-susp="${s.spec.suspend ? '1' : '0'}" title="${s.spec.suspend ? 'Resume' : 'Pause'}">${s.spec.suspend ? ic('play') : ic('pause')}</button>
                <button class="agents-iconbtn agents-iconbtn-danger" data-delsched="${escapeHTML(s.metadata.name)}" title="Delete">${ic('trash')}</button>
              </td>
            </tr>`
          })
          .join('')
  return `
    <div class="agents-panel">
      <div class="agents-panel-head"><h3>Schedules</h3>${showForm ? '' : `<button data-newobj ${vc.store.agents.length ? '' : 'disabled title="Create an agent first"'}>${ic('plus')} New schedule</button>`}</div>
      <p class="muted">Recurring or one-shot tasks${scoped ? ' that run as this agent' : '. Each runs as a specific agent'}. ${vc.store.agents.length ? '' : 'Create an agent first.'}</p>
      <table class="agents-table">
        <thead><tr><th>Name</th>${scoped ? '' : '<th>Agent</th>'}<th>When</th><th>Status</th><th class="agents-th-actions">Actions</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
      ${showForm ? formHTML(vc, editing, agentRef) : ''}
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement, agentRef?: string): void {
  root.querySelector<HTMLElement>('[data-newobj]')?.addEventListener('click', () => {
    creating = true
    editName = null
    vc.rerender()
  })
  root.querySelectorAll<HTMLElement>('[data-goagent]').forEach((el) =>
    el.addEventListener('click', () => vc.navigate({ kind: 'agent', name: el.dataset.goagent!, tab: 'flow' })),
  )
  root.querySelectorAll<HTMLElement>('[data-runsched]').forEach((el) => el.addEventListener('click', () => void runSchedule(vc, el.dataset.runsched!)))
  root.querySelectorAll<HTMLElement>('[data-editsched]').forEach((el) =>
    el.addEventListener('click', () => {
      editName = el.dataset.editsched!
      creating = false
      vc.rerender()
    }),
  )
  root.querySelectorAll<HTMLElement>('[data-suspsched]').forEach((el) =>
    el.addEventListener('click', () => void updateSchedule(vc, el.dataset.suspsched!, { suspend: el.dataset.susp !== '1' }, el.dataset.susp === '1' ? 'Schedule resumed.' : 'Schedule paused.')),
  )
  root.querySelectorAll<HTMLElement>('[data-delsched]').forEach((el) =>
    el.addEventListener('click', async () => {
      if (await confirmModal({ title: `Delete schedule “${el.dataset.delsched}”?`, danger: true, confirmLabel: 'Delete' })) void deleteSchedule(vc, el.dataset.delsched!)
    }),
  )
  root.querySelector<HTMLElement>('[data-objcancel]')?.addEventListener('click', () => {
    creating = false
    editName = null
    vc.rerender()
  })
  const form = root.querySelector<HTMLFormElement>('.agents-obj-form')
  if (form) {
    // Show cron OR runAt depending on the type select.
    const typeSel = form.querySelector<HTMLSelectElement>('select[name=type]')
    const cronL = form.querySelector<HTMLElement>('.agents-when-cron')
    const wakeL = form.querySelector<HTMLElement>('.agents-when-wakeup')
    const applyType = () => {
      const w = typeSel?.value === 'wakeup'
      if (cronL) cronL.style.display = w ? 'none' : ''
      if (wakeL) wakeL.style.display = w ? '' : 'none'
    }
    typeSel?.addEventListener('change', applyType)
    applyType()
  }
  form?.addEventListener('submit', (e) => {
    e.preventDefault()
    const g = (n: string) => (form.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
    const suspend = !!form.querySelector<HTMLInputElement>('input[name=suspend]')?.checked
    const type = g('type') || 'cron'
    const patch: Record<string, unknown> = { type, timeZone: g('timeZone'), task: g('task'), suspend }
    if (type === 'wakeup') patch.runAt = g('runAt')
    else patch.schedule = g('schedule')
    const edit = form.dataset.edit
    if (edit) {
      void updateSchedule(vc, edit, patch)
      editName = null
    } else {
      const name = g('name')
      const ref = agentRef || g('agentRef')
      if (!name || !ref) return
      void createSchedule(vc, { name, agentRef: ref, ...patch }).then((ok) => {
        if (ok) creating = false
      })
    }
  })
}
