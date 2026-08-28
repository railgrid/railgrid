// One parameterized component for schedules and triggers.
//
// They are the same object from the UI's point of view — "something that wakes
// this agent with a task, can be paused, run now, and routed to a channel" —
// and used to be two ~90%-identical views. The only real differences are the
// "when" fields (cron/runAt vs source/connection) and the status line, so those
// are the only places this file branches on `kind`.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon, type IconName } from '../ui/icon'
import { errorState } from '../ui/states'
import { toast } from '../ui/toast'
import { confirmModal } from '../portalkit/modal'
import { mutate } from '../mutate'
import { fmtTime, type Schedule, type ScheduleCreate, type SchedulePatch, type Trigger, type TriggerCreate, type TriggerPatch } from '../types'

export type AutomationKind = 'schedule' | 'trigger'

interface Meta {
  title: string
  one: string
  glyph: IconName
  blurb: string
  empty: string
  whenHeader: string
  namePlaceholder: string
  taskPlaceholder: string
}

const META: Record<AutomationKind, Meta> = {
  schedule: {
    title: 'Schedules',
    one: 'schedule',
    glyph: 'clock',
    blurb: 'Recurring or one-shot tasks that run as this agent, in the background.',
    empty: 'No schedules yet.',
    whenHeader: 'When',
    namePlaceholder: 'daily-digest',
    taskPlaceholder: "Summarise today's open PRs and post to my channel.",
  },
  trigger: {
    title: 'Triggers',
    one: 'trigger',
    glyph: 'zap',
    blurb: 'External events that wake this agent — a webhook POST or a GitHub event.',
    empty: 'No triggers yet.',
    whenHeader: 'Source',
    namePlaceholder: 'on-issue',
    taskPlaceholder: 'Triage the incoming event.',
  },
}

// Draft is the union of both forms' editable fields; only the ones the active
// kind renders are ever read back.
interface Draft {
  name: string
  type: string
  schedule: string
  runAt: string
  timeZone: string
  source: string
  connectionRef: string
  task: string
  channelRef: string
  suspend: boolean
}

const EMPTY: Draft = {
  name: '',
  type: 'cron',
  schedule: '',
  runAt: '',
  timeZone: '',
  source: 'webhook',
  connectionRef: '',
  task: '',
  channelRef: '',
  suspend: false,
}

export class AutomationSection extends StoreElement {
  @property({ type: String }) kind: AutomationKind = 'schedule'
  @property({ type: String }) agent = ''

  // editing: null = closed, '' = creating, name = editing that row.
  @state() private editing: string | null = null
  @state() private draft: Draft = { ...EMPTY }
  @state() private nameError = ''

  private get meta(): Meta {
    return META[this.kind]
  }

  private get rows(): (Schedule | Trigger)[] {
    const all = this.kind === 'schedule' ? this.store.schedules.data : this.store.triggers.data
    return all.filter((r) => r.spec.agentRef === this.agent)
  }

  private get slice() {
    return this.kind === 'schedule' ? this.store.schedules : this.store.triggers
  }

  private openCreate(): void {
    this.draft = { ...EMPTY }
    this.nameError = ''
    this.editing = ''
  }

  private openEdit(row: Schedule | Trigger): void {
    const s = row.spec as Schedule['spec'] & Trigger['spec']
    this.draft = {
      name: row.metadata.name,
      type: s.type || 'cron',
      schedule: s.schedule || '',
      runAt: s.runAt || '',
      timeZone: s.timeZone || '',
      source: s.source || 'webhook',
      connectionRef: s.connectionRef || '',
      task: s.task || s.checklist || '',
      channelRef: s.channelRef || '',
      suspend: !!s.suspend,
    }
    this.nameError = ''
    this.editing = row.metadata.name
  }

  private patch(): SchedulePatch | TriggerPatch {
    const d = this.draft
    if (this.kind === 'schedule') {
      const p: SchedulePatch = { type: d.type, timeZone: d.timeZone, task: d.task, suspend: d.suspend, channelRef: d.channelRef }
      if (d.type === 'wakeup') p.runAt = d.runAt
      else p.schedule = d.schedule
      return p
    }
    return { source: d.source || 'webhook', connectionRef: d.connectionRef, task: d.task, suspend: d.suspend, channelRef: d.channelRef }
  }

  private async save(e: Event): Promise<void> {
    e.preventDefault()
    const editingName = this.editing
    if (editingName) {
      const res = await mutate(this.store, {
        run: (): Promise<Schedule | Trigger> =>
          this.kind === 'schedule'
            ? this.api.patchSchedule(editingName, this.patch())
            : this.api.patchTrigger(editingName, this.patch()),
        success: `${cap(this.meta.one)} saved.`,
        failure: 'Save failed',
        reload: [this.kind === 'schedule' ? 'schedules' : 'triggers'],
      })
      if (res) this.editing = null
      return
    }
    const name = this.draft.name.trim()
    if (!name) {
      this.nameError = 'A name is required.'
      return
    }
    const body = { name, agentRef: this.agent, ...this.patch() }
    const res = await mutate(this.store, {
      run: (): Promise<Schedule | Trigger> =>
        this.kind === 'schedule'
          ? this.api.createSchedule(body as ScheduleCreate)
          : this.api.createTrigger(body as TriggerCreate),
      success: `${cap(this.meta.one)} “${name}” created.`,
      failure: 'Create failed',
      reload: [this.kind === 'schedule' ? 'schedules' : 'triggers'],
    })
    if (res) this.editing = null
  }

  private async toggleSuspend(row: Schedule | Trigger): Promise<void> {
    const name = row.metadata.name
    const next = !row.spec.suspend
    await mutate(this.store, {
      run: (): Promise<Schedule | Trigger> =>
        this.kind === 'schedule' ? this.api.patchSchedule(name, { suspend: next }) : this.api.patchTrigger(name, { suspend: next }),
      success: next ? `${cap(this.meta.one)} paused.` : `${cap(this.meta.one)} resumed.`,
      failure: 'Update failed',
      optimistic: () => (row.spec.suspend = next),
      rollback: () => (row.spec.suspend = !next),
      reload: [this.kind === 'schedule' ? 'schedules' : 'triggers'],
    })
  }

  private async del(name: string): Promise<void> {
    const authority = this.captureAuthority()
    const kind = this.kind
    const one = META[kind].one
    const ok = await confirmModal({ title: `Delete ${one} “${name}”?`, danger: true, confirmLabel: 'Delete' })
    if (!ok || !this.authorityIsCurrent(authority)) return
    await mutate(authority.store, {
      run: () => (kind === 'schedule' ? authority.api.deleteSchedule(name) : authority.api.deleteTrigger(name)),
      success: `${cap(one)} deleted.`,
      failure: 'Delete failed',
      reload: [kind === 'schedule' ? 'schedules' : 'triggers'],
    })
  }

  // runNow is asynchronous: the API returns 202 + runID and the work happens in
  // the executor, so the feedback is a link into the trace rather than a
  // truncated result string. We don't auto-navigate — the user asked to fire
  // it, not to leave the page — so the toast action is the opt-in.
  private async runNow(name: string): Promise<void> {
    const res = await mutate(this.store, {
      run: (): Promise<{ runID: string }> => (this.kind === 'schedule' ? this.api.runSchedule(name) : this.api.runTrigger(name)),
      failure: 'Run failed',
    })
    if (!res?.runID) return
    const id = res.runID
    toast('ok', `${name} queued.`, { label: 'View run', run: () => this.navigate({ kind: 'run', id }) })
  }

  render(): TemplateResult {
    const rows = this.rows
    const slice = this.slice
    return html`<section class="agents-panel k-card agents-config-sec">
      <div class="agents-panel-head">
        <h3>${icon(this.meta.glyph)} ${this.meta.title}</h3>
        ${this.editing === null
          ? html`<button class="k-btn k-btn--ghost secondary" @click=${() => this.openCreate()}>${icon('plus')} New ${this.meta.one}</button>`
          : nothing}
      </div>
      <p class="muted">${this.meta.blurb}</p>
      ${slice.error
        ? errorState(slice.error, () => void this.store.load(this.kind === 'schedule' ? 'schedules' : 'triggers'))
        : rows.length === 0
          ? html`<p class="agents-hint">${icon(this.meta.glyph)} ${this.meta.empty}</p>`
          : html`<div class="agents-tablewrap k-table">
              <table class="agents-table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>${this.meta.whenHeader}</th>
                    <th>Status</th>
                    <th class="agents-th-actions">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  ${repeat(
                    rows,
                    (r) => r.metadata.name,
                    (r) => this.row(r),
                  )}
                </tbody>
              </table>
            </div>`}
      ${this.editing !== null ? this.form() : nothing}
    </section>`
  }

  private row(r: Schedule | Trigger): TemplateResult {
    const name = r.metadata.name
    const s = r.spec as Schedule['spec'] & Trigger['spec']
    const st = r.status as (Schedule['status'] & Trigger['status']) | undefined
    const when =
      this.kind === 'schedule'
        ? html`${s.type === 'wakeup' ? s.runAt || '' : s.schedule || ''}${s.timeZone ? html` <span class="muted">${s.timeZone}</span>` : nothing}`
        : html`${s.source}${s.connectionRef ? html` <span class="muted">${s.connectionRef}</span>` : nothing}`
    const status = st?.disabledReason
      ? html`<span class="k-badge agents-badge k-badge--warning agents-badge-warn">${st.disabledReason}</span>`
      : s.suspend
        ? html`<span class="k-badge agents-badge">paused</span>`
        : this.kind === 'schedule'
          ? st?.nextRun
            ? html`next ${fmtTime(st.nextRun)}`
            : html`armed`
          : st?.lastFired
            ? html`last ${fmtTime(st.lastFired)}`
            : html`armed`
    const lastRunID = st?.lastRunID
    return html`<tr class=${this.editing === name ? 'is-editing' : ''}>
      <td><strong>${name}</strong></td>
      <td class="mono">${when}</td>
      <td class="muted">
        ${status}
        ${lastRunID
          ? html` <button class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'run', id: lastRunID })}>last run</button>`
          : nothing}
      </td>
      <td class="agents-row-actions">
        <button class="k-btn k-btn--ghost agents-iconbtn" aria-label="Run ${name} now" title="Run now" @click=${() => void this.runNow(name)}>
          ${icon('play')}
        </button>
        <button class="k-btn k-btn--ghost agents-iconbtn" aria-label="Edit ${name}" title="Edit" @click=${() => this.openEdit(r)}>${icon('pencil')}</button>
        <button
          class="k-btn k-btn--ghost agents-iconbtn"
          aria-label=${s.suspend ? `Resume ${name}` : `Pause ${name}`}
          title=${s.suspend ? 'Resume' : 'Pause'}
          @click=${() => void this.toggleSuspend(r)}
        >
          ${s.suspend ? icon('play') : icon('pause')}
        </button>
        <button
          class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger"
          aria-label="Delete ${name}"
          title="Delete"
          @click=${() => void this.del(name)}
        >
          ${icon('trash')}
        </button>
      </td>
    </tr>`
  }

  private form(): TemplateResult {
    const d = this.draft
    const isEdit = !!this.editing
    const set = <K extends keyof Draft>(k: K) => (e: Event) => {
      const t = e.target as HTMLInputElement
      this.draft = { ...this.draft, [k]: t.type === 'checkbox' ? t.checked : t.value }
    }
    const channels = this.store.agent(this.agent)?.spec?.channels || []
    return html`<form class="agents-obj-form k-card" @submit=${(e: Event) => void this.save(e)}>
      <h4>${isEdit ? `Edit ${this.meta.one} ${this.editing}` : `New ${this.meta.one}`}</h4>
      ${isEdit
        ? nothing
        : html`<label>
            Name *
            <input class="k-input" .value=${d.name} placeholder=${this.meta.namePlaceholder} autocomplete="off" @input=${set('name')} />
            ${this.nameError ? html`<span class="agents-fielderr">${this.nameError}</span>` : nothing}
          </label>`}
      ${this.kind === 'schedule'
        ? html`
            <div class="agents-grid2">
              <label>
                Type
                <select class="k-input" @change=${set('type')}>
                  <option value="cron" ?selected=${d.type !== 'wakeup'}>recurring (cron)</option>
                  <option value="wakeup" ?selected=${d.type === 'wakeup'}>one-shot (runAt)</option>
                </select>
              </label>
              <label>Timezone<input class="k-input" .value=${d.timeZone} placeholder="Europe/Vilnius" @input=${set('timeZone')} /></label>
            </div>
            ${d.type === 'wakeup'
              ? html`<label
                  >Run at (RFC3339)<input class="k-input mono" .value=${d.runAt} placeholder="2026-01-01T09:00:00Z" @input=${set('runAt')} />
                </label>`
              : html`<label
                  >Cron<input class="k-input mono" .value=${d.schedule} placeholder="0 9 * * *" @input=${set('schedule')} />
                  <span class="agents-hint">5-field cron · crontab.guru</span>
                </label>`}
          `
        : html`<div class="agents-grid2">
            <label>
              Source
              <select class="k-input" @change=${set('source')}>
                ${['webhook', 'github'].map((v) => html`<option value=${v} ?selected=${v === d.source}>${v}</option>`)}
              </select>
            </label>
            <label>
              Connection
              <select class="k-input" @change=${set('connectionRef')}>
                <option value="">— none —</option>
                ${this.store.connections.data.map(
                  (c) => html`<option value=${c.metadata.name} ?selected=${c.metadata.name === d.connectionRef}>${c.metadata.name}</option>`,
                )}
              </select>
            </label>
          </div>`}
      <label>
        Task${this.kind === 'trigger' ? ' on fire' : ''}
        <textarea class="k-input" rows="3" placeholder=${this.meta.taskPlaceholder} .value=${d.task} @input=${set('task')}></textarea>
      </label>
      <label>
        Channel
        <select class="k-input" @change=${set('channelRef')}>
          <option value="" ?selected=${!d.channelRef}>— primary channel —</option>
          ${channels.map(
            (ch) => html`<option value=${ch.name} ?selected=${ch.name === d.channelRef}>${ch.name}${ch.primary ? ' (primary)' : ''}</option>`,
          )}
        </select>
        <span class="agents-hint">Where output is delivered</span>
      </label>
      <label class="agents-check"><input type="checkbox" .checked=${d.suspend} @change=${set('suspend')} /> Paused</label>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="submit">${isEdit ? 'Save' : `Create ${this.meta.one}`}</button>
        <button type="button" class="k-btn k-btn--ghost secondary" @click=${() => (this.editing = null)}>Cancel</button>
      </div>
    </form>`
  }
}

const cap = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1)

if (!customElements.get('agents-automation')) customElements.define('agents-automation', AutomationSection)
