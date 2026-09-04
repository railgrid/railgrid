// Run trace viewer: the header (agent, trigger, session, usage, attempt), the
// step timeline of tool calls with expandable args/result, delegated child runs,
// error detail, Cancel while running, and approve/deny for a run paused on a
// gate (which resumes it server-side).

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState, loadingState, staleState } from '../ui/states'
import { toast } from '../ui/toast'
import { attachCodeCopy, renderMarkdown } from '../ui/markdown'
import { fmtDuration, fmtTime, fmtTokens, fmtUSD, prettyJSON, type RunDetail as Run, type RunStep, type RunSummary } from '../types'
import type { ServerEvent } from '../store'
import { phaseChip } from './activity'

const LIVE_PHASES = new Set(['Pending', 'Running', 'PendingApproval'])

function isNestedControl(event: Event): boolean {
  const currentTarget = event.currentTarget as Element | null
  const target = event.target as Element | null
  const control = target?.closest?.(
    'a, button, input, select, textarea, summary, [contenteditable="true"], [role="button"], [role="link"]',
  )
  return Boolean(control && control !== currentTarget)
}

export class RunDetailView extends StoreElement {
  @property({ type: String }) runID = ''

  @state() private run: Run | null = null
  @state() private error: string | null = null
  @state() private expanded = new Set<string>()

  private loadedFor = ''
  // A live run publishes an event when it starts and when it finishes, and
  // nothing in between — so an event subscription alone leaves this view frozen
  // for the whole run. Poll while it is live: it is also the only thing that
  // works when the run executes on another replica, since the event bus is
  // in-process.
  private pollHandle = 0
  private tickHandle = 0
  private requestGeneration = 0
  @state() private now = Date.now()

  connectedCallback(): void {
    super.connectedCallback()
    this.store?.addEventListener('server', this.onServerEvent as EventListener)
  }

  disconnectedCallback(): void {
    this.store?.removeEventListener('server', this.onServerEvent as EventListener)
    this.requestGeneration += 1
    this.stopLive()
    super.disconnectedCallback()
  }

  // syncLive starts or stops the pollers to match the run's phase, so a finished
  // run costs nothing and a live one always shows its own progress.
  private syncLive(r: Run | null): void {
    const live = !!r && LIVE_PHASES.has(r.phase)
    if (!live) {
      this.stopLive()
      return
    }
    if (!this.pollHandle) {
      this.pollHandle = window.setInterval(() => void this.load('background'), 3000)
    }
    if (!this.tickHandle) {
      // Separate, faster tick purely for the elapsed-time readout: a clock that
      // does not move is the clearest possible signal that a page is dead.
      this.tickHandle = window.setInterval(() => (this.now = Date.now()), 1000)
    }
  }

  private stopLive(): void {
    if (this.pollHandle) window.clearInterval(this.pollHandle)
    if (this.tickHandle) window.clearInterval(this.tickHandle)
    this.pollHandle = 0
    this.tickHandle = 0
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
    // Refresh on this run's own transitions and on any child's. Matching
    // parentRunID — not just children already loaded — is what makes a worker
    // appear the moment it starts rather than on the next manual reload.
    const mine =
      ev.data.id === this.runID ||
      ev.data.parentRunID === this.runID ||
      this.run?.children?.some((c) => c.id === ev.data.id)
    if (ev.type === 'run' && mine) void this.load('background')
    if (ev.type === 'inbox' && ev.data.runID === this.runID) void this.load('background')
  }

  protected updated(): void {
    attachCodeCopy(this)
  }

  private async load(_mode: 'foreground' | 'background' = 'foreground'): Promise<void> {
    const requestedRunID = this.runID
    const requestGeneration = ++this.requestGeneration
    try {
      const run = await this.api.getRun(requestedRunID)
      if (requestGeneration !== this.requestGeneration || requestedRunID !== this.runID) return
      this.run = run
      this.error = null
    } catch (e) {
      if (requestGeneration !== this.requestGeneration || requestedRunID !== this.runID) return
      this.error = (e as Error).message
    }
    if (requestGeneration === this.requestGeneration) this.syncLive(this.run)
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
    const back = html`<button class="k-btn k-btn--ghost agents-back" @click=${() => this.navigate({ kind: 'menu', menu: 'activity' })}>
      ${icon('arrow-left')} Activity
    </button>`
    if (this.error && !this.run) {
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
          ${LIVE_PHASES.has(r.phase) ? html`<button class="k-btn k-btn--ghost secondary" @click=${() => void this.cancel()}>${icon('x')} Cancel run</button>` : nothing}
          <button class="k-btn k-btn--ghost secondary" @click=${() => void this.load()}>${icon('refresh')} Refresh</button>
        </div>
      </div>
      ${this.error ? staleState(this.error, () => void this.load('foreground')) : nothing}
      ${this.header(r)} ${this.pending(r)} ${this.output(r)} ${this.timeline(r)} ${this.childRuns(r)}
    </div>`
  }

  private header(r: Run): TemplateResult {
    const cell = (k: string, v: unknown): TemplateResult => html`<div class="agents-runmeta-cell">
      <span class="agents-runmeta-k">${k}</span><span class="agents-runmeta-v">${v}</span>
    </div>`
    return html`<section class="agents-panel k-card">
      <div class="agents-runmeta">
        ${cell(
          'agent',
          html`<button class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'agent', name: r.agent, tab: 'config' })}>${r.agent}</button>`,
        )}
        ${cell('trigger', html`<span class="mono">${r.trigger}</span> <span class="muted">(${r.class})</span>`)}
        ${r.sessionID ? cell('session', html`<span class="mono">${r.sessionID}</span>`) : nothing}
        ${cell('started', fmtTime(r.startedAt || r.createdAt))}
        ${cell(
          'duration',
          r.durationMS
            ? fmtDuration(r.durationMS)
            : LIVE_PHASES.has(r.phase)
              ? html`<span class="agents-elapsed"
                  ><span class="agents-spinner" aria-hidden="true"></span>${fmtDuration(
                    Math.max(0, this.now - new Date(r.startedAt || r.createdAt).getTime()),
                  )}</span
                >`
              : '—',
        )}
        ${cell('usage', `${fmtTokens(r.inputTokens)} in · ${fmtTokens(r.outputTokens)} out · ${fmtUSD(r.usdMicros)}`)}
        ${r.attempt && r.attempt > 1 ? cell('attempt', String(r.attempt)) : nothing}
        ${r.parentRunID
          ? cell(
              'parent',
              html`<button class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'run', id: r.parentRunID as string })}>
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
    return html`<section class="agents-panel k-card">
      <div class="agents-approval" role="group" aria-label="Tool approval required">
        <div class="agents-approval-head">${icon('key')} Paused — approval required for <span class="mono">${p.tool}</span></div>
        ${p.args ? html`<pre class="agents-approval-args">${prettyJSON(p.args)}</pre>` : nothing}
        <div class="agents-approval-actions">
          <button class="k-btn k-btn--primary" @click=${() => void this.resolve(p.inboxID, 'approve')}>${icon('check')} Approve &amp; resume</button>
          <button class="k-btn k-btn--ghost secondary" @click=${() => void this.resolve(p.inboxID, 'deny')}>${icon('x')} Deny</button>
        </div>
      </div>
    </section>`
  }

  private output(r: Run): TemplateResult | typeof nothing {
    const failed = r.phase === 'Failed' || r.phase === 'Aborted'
    // The answer lives on the run record; message carries the failure reason. A
    // failed run can have both (it produced text, then broke).
    if (failed && r.message) {
      return html`<section class="agents-panel k-card">
        <h3>${icon('x')} Error</h3>
        <div class="agents-err" role="alert">${r.message}</div>
        ${r.output ? html`<h3>${icon('message')} Partial output</h3>
            <div class="agents-body">${renderMarkdown(r.output)}</div>` : nothing}
      </section>`
    }
    const body = r.output || r.message
    if (!body) return nothing
    return html`<section class="agents-panel k-card">
      <h3>${icon('message')} Output</h3>
      <div class="agents-body">${renderMarkdown(body)}</div>
      ${r.sources?.length
        ? html`<div class="agents-runsources">
            <span class="agents-runmeta-k">sources</span>
            <ul>
              ${r.sources.map((s) => html`<li><a href=${s} target="_blank" rel="noopener noreferrer">${s}</a></li>`)}
            </ul>
          </div>`
        : nothing}
    </section>`
  }

  private timeline(r: Run): TemplateResult {
    return html`<section class="agents-panel k-card">
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
      <button class="k-btn k-btn--ghost agents-step-head" aria-expanded=${open ? 'true' : 'false'} @click=${() => this.toggle(s.id)}>
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

  // fanOutGranted reports whether this run could have spawned workers at all,
  // read from the agent's grant for the run's own class. Without it an empty
  // fan-out section is unreadable: "no workers" and "fan-out was never available"
  // look identical, and that is exactly the question a user has.
  private fanOutGranted(r: Run): boolean {
    const tools = this.store.agent(r.agent)?.spec?.tools
    const fams = (r.class === 'interactive' ? tools?.interactive : tools?.background)?.families || []
    return fams.includes('spawn')
  }

  private childRuns(r: Run): TemplateResult | typeof nothing {
    const granted = this.fanOutGranted(r)
    if (!r.children?.length) {
      // Say something whenever fan-out was on the table, so a run that could have
      // used it and did not is a fact rather than an absence.
      if (!granted) return nothing
      const tried = r.steps.some((s) => s.tool === 'spawn')
      return html`<section class="agents-panel k-card">
        <h3>${icon('corner-down-right')} Child runs <span class="muted">(0)</span></h3>
        <p class="agents-hint">
          ${tried
            ? html`${icon('circle')} This run called <span class="mono">spawn</span> but no worker runs were recorded — check the
                steps above for the error it came back with.`
            : html`${icon('circle')} Research fan-out is enabled and the agent was told how to use it, but it answered this request
                directly rather than splitting it up. That is the right call for a narrow question — a fan-out you do not need is just
                slower. For a request with genuinely independent parts, phrasing them explicitly ("compare X, Y and Z") makes the split
                obvious.`}
        </p>
      </section>`
    }
    // Children are spawned workers and delegations alike, so the heading names
    // the relationship rather than one of the two mechanisms.
    const kids = r.children
    const workers = kids.filter((c) => c.trigger === 'spawn').length
    // A fan-out runs a few workers at a time and queues the rest, so "running"
    // and "queued" are different facts: without the split, a queued worker is
    // indistinguishable from one that is stuck.
    const running = kids.filter((c) => c.phase === 'Running').length
    const queued = kids.filter((c) => c.phase === 'Pending').length
    const done = kids.filter((c) => c.phase === 'Succeeded').length
    const problems = kids.filter((c) => c.phase === 'Failed' || c.phase === 'Aborted').length
    const waiting = kids.filter((c) => c.phase === 'PendingApproval').length
    const parts = [
      running ? `${running} running` : '',
      queued ? `${queued} queued` : '',
      waiting ? `${waiting} awaiting approval` : '',
      done ? `${done} done` : '',
      problems ? `${problems} failed` : '',
    ].filter(Boolean)

    return html`<section class="agents-panel k-card">
      <h3>
        ${icon('corner-down-right')} Child runs <span class="muted">(${kids.length})</span>
        ${workers ? html`<span class="muted">· ${workers} spawned worker${workers === 1 ? '' : 's'}</span>` : nothing}
      </h3>
      <p class="agents-child-summary ${running + queued > 0 ? 'is-live' : ''}">
        ${running + queued > 0 ? html`<span class="agents-spinner" aria-hidden="true"></span>` : nothing}
        ${parts.join(' · ')}
        ${running + queued > 0
          ? html`<span class="muted">— this updates as they finish</span>`
          : nothing}
      </p>
      <div class="agents-tablewrap k-table">
        <table class="agents-table">
          <thead>
            <tr><th>Agent</th><th>Kind</th><th>Input</th><th>Phase</th><th>Duration</th><th>Usage</th></tr>
          </thead>
          <tbody>
            ${r.children.map((c: RunSummary) => html`<tr class="agents-run-row" tabindex="0" aria-label="Open run ${c.id}"
              @click=${(e: Event) => {
                if (!isNestedControl(e)) this.navigate({ kind: 'run', id: c.id })
              }}
              @keydown=${(e: KeyboardEvent) => {
                if (isNestedControl(e)) return
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  this.navigate({ kind: 'run', id: c.id })
                }
              }}
            >
              <td><strong>${c.agent}</strong></td>
              <td class="muted mono">${c.trigger === 'spawn' ? 'worker' : 'delegated'}</td>
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
