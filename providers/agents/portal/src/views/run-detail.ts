// Run trace viewer: the header (agent, trigger, session, usage, attempt), the
// step timeline of tool calls with expandable args/result, delegated child runs,
// error detail, Cancel while running, and approve/deny for a run paused on a
// gate (which resumes it server-side).

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState, loadingState } from '../ui/states'
import { toast } from '../ui/toast'
import { attachCodeCopy, renderMarkdown } from '../ui/markdown'
import { fmtDuration, fmtTime, fmtTokens, fmtUSD, prettyJSON, type RunDetail as Run, type RunStep, type RunSummary } from '../types'
import type { ServerEvent } from '../store'
import { phaseChip } from './activity'

const LIVE_PHASES = new Set(['Pending', 'Running', 'PendingApproval'])

export class RunDetailView extends StoreElement {
  @property({ type: String }) runID = ''

  @state() private run: Run | null = null
  @state() private error: string | null = null
  @state() private expanded = new Set<string>()

  private loadedFor = ''

  connectedCallback(): void {
    super.connectedCallback()
    this.store?.addEventListener('server', this.onServerEvent as EventListener)
  }

  disconnectedCallback(): void {
    this.store?.removeEventListener('server', this.onServerEvent as EventListener)
    super.disconnectedCallback()
  }

  protected willUpdate(): void {
    super.willUpdate()
    if (this.runID && this.loadedFor !== this.runID) {
      this.loadedFor = this.runID
      this.run = null
      void this.load()
    }
  }

  private onServerEvent = (e: Event): void => {
    const ev = (e as CustomEvent<ServerEvent>).detail
    // Refresh on this run's own transitions and on its children's (a delegation
    // finishing changes what the timeline shows).
    if (ev.type === 'run' && (ev.data.id === this.runID || this.run?.children?.some((c) => c.id === ev.data.id))) void this.load()
    if (ev.type === 'inbox' && ev.data.runID === this.runID) void this.load()
  }

  protected updated(): void {
    attachCodeCopy(this)
  }

  private async load(): Promise<void> {
    try {
      this.run = await this.api.getRun(this.runID)
      this.error = null
    } catch (e) {
      this.error = (e as Error).message
    }
  }

  private toggle(id: string): void {
    const next = new Set(this.expanded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    this.expanded = next
  }

  private async cancel(): Promise<void> {
    try {
      await this.api.cancelRun(this.runID)
      toast('ok', 'Cancelling the run…')
      void this.load()
    } catch (e) {
      toast('error', `Cancel failed: ${(e as Error).message}`)
    }
  }

  private async resolve(inboxID: string, decision: 'approve' | 'deny'): Promise<void> {
    try {
      await this.api.resolveInbox(inboxID, decision)
      toast('ok', decision === 'approve' ? 'Approved — the run is resuming.' : 'Denied.')
      void this.store.load('inbox')
      void this.load()
    } catch (e) {
      toast('error', `Could not ${decision}: ${(e as Error).message}`)
    }
  }

  render(): TemplateResult {
    const back = html`<button class="agents-back" @click=${() => this.navigate({ kind: 'menu', menu: 'activity' })}>
      ${icon('arrow-left')} Activity
    </button>`
    if (this.error) {
      return html`<div class="agents-detail">
        <div class="agents-detail-head"><div class="agents-detail-title">${back}</div></div>
        ${errorState(this.error, () => void this.load())}
      </div>`
    }
    const r = this.run
    if (!r) {
      return html`<div class="agents-detail">
        <div class="agents-detail-head"><div class="agents-detail-title">${back}</div></div>
        ${loadingState('Loading run…')}
      </div>`
    }
    return html`<div class="agents-detail">
      <div class="agents-detail-head">
        <div class="agents-detail-title">${back}<h2>Run ${phaseChip(r.phase)}</h2></div>
        <div class="agents-detail-actions">
          ${LIVE_PHASES.has(r.phase) ? html`<button class="secondary" @click=${() => void this.cancel()}>${icon('x')} Cancel run</button>` : nothing}
          <button class="secondary" @click=${() => void this.load()}>${icon('refresh')} Refresh</button>
        </div>
      </div>
      ${this.header(r)} ${this.pending(r)} ${this.output(r)} ${this.timeline(r)} ${this.childRuns(r)}
    </div>`
  }

  private header(r: Run): TemplateResult {
    const cell = (k: string, v: unknown): TemplateResult => html`<div class="agents-runmeta-cell">
      <span class="agents-runmeta-k">${k}</span><span class="agents-runmeta-v">${v}</span>
    </div>`
    return html`<section class="agents-panel">
      <div class="agents-runmeta">
        ${cell(
          'agent',
          html`<button class="agents-linkbtn" @click=${() => this.navigate({ kind: 'agent', name: r.agent, tab: 'config' })}>${r.agent}</button>`,
        )}
        ${cell('trigger', html`<span class="mono">${r.trigger}</span> <span class="muted">(${r.class})</span>`)}
        ${r.sessionID ? cell('session', html`<span class="mono">${r.sessionID}</span>`) : nothing}
        ${cell('started', fmtTime(r.startedAt || r.createdAt))}
        ${cell('duration', r.durationMS ? fmtDuration(r.durationMS) : '—')}
        ${cell('usage', `${fmtTokens(r.inputTokens)} in · ${fmtTokens(r.outputTokens)} out · ${fmtUSD(r.usdMicros)}`)}
        ${r.attempt && r.attempt > 1 ? cell('attempt', String(r.attempt)) : nothing}
        ${r.parentRunID
          ? cell(
              'parent',
              html`<button class="agents-linkbtn" @click=${() => this.navigate({ kind: 'run', id: r.parentRunID as string })}>
                ${r.parentRunID.slice(0, 8)}
              </button>`,
            )
          : nothing}
      </div>
      ${r.input ? html`<div class="agents-runinput"><span class="agents-runmeta-k">input</span><pre>${r.input}</pre></div>` : nothing}
    </section>`
  }

  private pending(r: Run): TemplateResult | typeof nothing {
    if (r.phase !== 'PendingApproval' || !r.pending) return nothing
    const p = r.pending
    return html`<section class="agents-panel">
      <div class="agents-approval" role="group" aria-label="Tool approval required">
        <div class="agents-approval-head">${icon('key')} Paused — approval required for <span class="mono">${p.tool}</span></div>
        ${p.args ? html`<pre class="agents-approval-args">${prettyJSON(p.args)}</pre>` : nothing}
        <div class="agents-approval-actions">
          <button @click=${() => void this.resolve(p.inboxID, 'approve')}>${icon('check')} Approve &amp; resume</button>
          <button class="secondary" @click=${() => void this.resolve(p.inboxID, 'deny')}>${icon('x')} Deny</button>
        </div>
      </div>
    </section>`
  }

  private output(r: Run): TemplateResult | typeof nothing {
    if (!r.message) return nothing
    const failed = r.phase === 'Failed' || r.phase === 'Aborted'
    return html`<section class="agents-panel">
      <h3>${failed ? html`${icon('x')} Error` : html`${icon('message')} Output`}</h3>
      ${failed ? html`<div class="agents-err" role="alert">${r.message}</div>` : html`<div class="agents-body">${renderMarkdown(r.message)}</div>`}
    </section>`
  }

  private timeline(r: Run): TemplateResult {
    return html`<section class="agents-panel">
      <h3>${icon('workflow')} Steps <span class="muted">(${r.steps.length})</span></h3>
      ${r.steps.length === 0
        ? html`<p class="agents-hint">${icon('wrench')} This run made no tool calls.</p>`
        : html`<ol class="agents-timeline">
            ${repeat(
              r.steps,
              (s) => s.id,
              (s, i) => this.step(s, i),
            )}
          </ol>`}
    </section>`
  }

  private step(s: RunStep, i: number): TemplateResult {
    const open = this.expanded.has(s.id)
    const cls = s.outcome === 'error' ? 'err' : s.outcome === 'pending_approval' ? 'wait' : 'ok'
    return html`<li class="agents-step is-${cls}">
      <button class="agents-step-head" aria-expanded=${open ? 'true' : 'false'} @click=${() => this.toggle(s.id)}>
        <span class="agents-step-n">${i + 1}</span>
        <span class="agents-step-name mono">${s.tool}</span>
        <span class="agents-step-meta">${s.outcome}${s.durationMS ? ` · ${fmtDuration(s.durationMS)}` : ''} · ${fmtTime(s.at)}</span>
        <span class="agents-toolcard-chev ${open ? 'open' : ''}">${icon('chevron-right')}</span>
      </button>
      ${open
        ? html`<div class="agents-step-body">
            ${s.args ? html`<div class="agents-kv"><span>args</span><pre>${prettyJSON(s.args)}</pre></div>` : nothing}
            ${s.error ? html`<div class="agents-kv"><span>error</span><pre class="err">${s.error}</pre></div>` : nothing}
            ${s.result ? html`<div class="agents-kv"><span>result</span><pre>${prettyJSON(s.result)}</pre></div>` : nothing}
          </div>`
        : nothing}
    </li>`
  }

  private childRuns(r: Run): TemplateResult | typeof nothing {
    if (!r.children?.length) return nothing
    return html`<section class="agents-panel">
      <h3>${icon('corner-down-right')} Delegated runs</h3>
      <div class="agents-tablewrap">
        <table class="agents-table">
          <thead>
            <tr><th>Agent</th><th>Input</th><th>Phase</th><th>Duration</th><th>Usage</th></tr>
          </thead>
          <tbody>
            ${r.children.map((c: RunSummary) => html`<tr class="agents-run-row" tabindex="0" role="link" aria-label="Open run ${c.id}"
              @click=${() => this.navigate({ kind: 'run', id: c.id })}
              @keydown=${(e: KeyboardEvent) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  this.navigate({ kind: 'run', id: c.id })
                }
              }}
            >
              <td><strong>${c.agent}</strong></td>
              <td class="agents-cell-task muted">${c.inputPreview || '—'}</td>
              <td>${phaseChip(c.phase)}</td>
              <td class="muted">${c.durationMS ? fmtDuration(c.durationMS) : '—'}</td>
              <td class="muted mono">${fmtTokens(c.inputTokens + c.outputTokens)} · ${fmtUSD(c.usdMicros)}</td>
            </tr>`)}
          </tbody>
        </table>
      </div>
    </section>`
  }
}

if (!customElements.get('agents-run-detail')) customElements.define('agents-run-detail', RunDetailView)
