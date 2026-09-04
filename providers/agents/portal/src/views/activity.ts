// Activity: the run feed. Pending approvals are pinned at the top (this tab
// absorbed the standalone Inbox), then a paged, filterable run table. A run row
// opens the trace viewer.
//
// The list stays live off /api/events: a `run` push refreshes the first page
// (debounced) rather than polling.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon, type IconName } from '../ui/icon'
import { emptyState, errorState, loadingState, staleState } from '../ui/states'
import { toast } from '../ui/toast'
import { fmtDuration, fmtTime, fmtTokens, fmtUSD, type InboxItem, type RunPhase, type RunSummary } from '../types'
import type { RunFilter } from '../api'
import type { ServerEvent } from '../store'

export const PHASE_META: Record<RunPhase, { label: string; cls: string; glyph: IconName }> = {
  Pending: { label: 'Pending', cls: 'pending', glyph: 'clock' },
  Running: { label: 'Running', cls: 'running', glyph: 'play' },
  PendingApproval: { label: 'Needs approval', cls: 'approval', glyph: 'key' },
  Succeeded: { label: 'Succeeded', cls: 'ok', glyph: 'check' },
  Failed: { label: 'Failed', cls: 'failed', glyph: 'x' },
  Aborted: { label: 'Aborted', cls: 'aborted', glyph: 'pause' },
}

export function phaseChip(phase: RunPhase): TemplateResult {
  const m = PHASE_META[phase] || { label: phase, cls: 'pending', glyph: 'circle' as IconName }
  const tone = m.cls === 'ok' || m.cls === 'running' ? 'k-badge--success' : m.cls === 'failed' || m.cls === 'aborted' ? 'k-badge--danger' : 'k-badge--warning'
  return html`<span class="k-badge ${tone} agents-phase agents-phase-${m.cls}">${icon(m.glyph)} ${m.label}</span>`
}

const PHASES: RunPhase[] = ['Pending', 'Running', 'PendingApproval', 'Succeeded', 'Failed', 'Aborted']
const PAGE = 50

// Presets rather than two date pickers: "runs since N ago" is the question
// people actually ask of an execution feed, and it is one keystroke instead of
// two calendar dialogs. `hours: 0` means unbounded.
const RANGES: { id: string; label: string; hours: number }[] = [
  { id: '24h', label: '24h', hours: 24 },
  { id: '7d', label: '7d', hours: 24 * 7 },
  { id: '30d', label: '30d', hours: 24 * 30 },
  { id: 'all', label: 'All', hours: 0 },
]

export class Activity extends StoreElement {
  // agent scopes the feed to one agent (the agent detail "Runs" tab); when set,
  // the agent filter is fixed and hidden.
  @property({ type: String }) agent = ''

  @state() private runs: RunSummary[] = []
  @state() private nextCursor = ''
  @state() private loading = false
  @state() private error: string | null = null
  @state() private loaded = false
  @state() private filterAgent = ''
  @state() private filterClass = ''
  @state() private filterPhase = ''
  // Defaults to unbounded so the feed never silently hides a run the user just
  // fired and then went looking for.
  @state() private range = 'all'

  private started = false
  private refreshTimer = 0
  private requestGeneration = 0

  connectedCallback(): void {
    super.connectedCallback()
    this.store?.addEventListener('server', this.onServerEvent as EventListener)
  }

  disconnectedCallback(): void {
    this.store?.removeEventListener('server', this.onServerEvent as EventListener)
    if (this.refreshTimer) clearTimeout(this.refreshTimer)
    this.requestGeneration += 1
    super.disconnectedCallback()
  }

  protected willUpdate(): void {
    super.willUpdate()
    if (!this.started && this.api) {
      this.started = true
      void this.reload('foreground')
    }
  }

  // A burst of phase transitions (Running → Succeeded on several runs) should
  // cost one refetch, not one per event.
  private onServerEvent = (e: Event): void => {
    if ((e as CustomEvent<ServerEvent>).detail.type !== 'run') return
    if (this.refreshTimer) return
    this.refreshTimer = window.setTimeout(() => {
      this.refreshTimer = 0
      void this.reload('background')
    }, 700)
  }

  private filter(cursor?: string): RunFilter {
    const hours = RANGES.find((r) => r.id === this.range)?.hours || 0
    return {
      agent: this.agent || this.filterAgent || undefined,
      class: this.filterClass || undefined,
      phase: this.filterPhase || undefined,
      since: hours ? new Date(Date.now() - hours * 3600_000).toISOString() : undefined,
      cursor,
      limit: PAGE,
    }
  }

  private async reload(mode: 'foreground' | 'background' = 'foreground'): Promise<void> {
    // Event-driven refreshes are deliberately quiet once a snapshot exists.
    // Manual refresh/filter changes remain foreground work and keep their
    // existing explicit pending feedback.
    if (mode === 'foreground' || !this.loaded) this.loading = true
    const requestGeneration = ++this.requestGeneration
    try {
      const page = await this.api.listRuns(this.filter())
      if (requestGeneration !== this.requestGeneration) return
      this.runs = page.items
      this.nextCursor = page.nextCursor || ''
      this.error = null
      this.loaded = true
    } catch (e) {
      if (requestGeneration !== this.requestGeneration) return
      this.error = (e as Error).message
    } finally {
      if (requestGeneration === this.requestGeneration) this.loading = false
    }
  }

  private async more(): Promise<void> {
    if (!this.nextCursor || this.loading) return
    this.loading = true
    const requestGeneration = ++this.requestGeneration
    try {
      const page = await this.api.listRuns(this.filter(this.nextCursor))
      if (requestGeneration !== this.requestGeneration) return
      this.runs = [...this.runs, ...page.items]
      this.nextCursor = page.nextCursor || ''
    } catch (e) {
      if (requestGeneration === this.requestGeneration) {
        toast('error', `Could not load more runs: ${(e as Error).message}`)
      }
    } finally {
      if (requestGeneration === this.requestGeneration) this.loading = false
    }
  }

  private async resolve(item: InboxItem, decision: 'approve' | 'deny'): Promise<void> {
    try {
      await this.api.resolveInbox(item.id, decision)
      toast('ok', decision === 'approve' ? 'Approved — the run is resuming.' : 'Denied.')
      void this.store.load('inbox')
      void this.reload('foreground')
    } catch (e) {
      toast('error', `Could not ${decision}: ${(e as Error).message}`)
    }
  }

  render(): TemplateResult {
    return html`<div class="agents-panel k-card agents-route-panel agents-activity-panel">
      ${this.agent
        ? nothing
        : html`<div class="agents-panel-head agents-activity-head"><h3>Activity</h3></div>`}
      ${this.approvals()} ${this.filters()} ${this.table()}
    </div>`
  }

  // ---- pinned approvals ----------------------------------------------------

  private approvals(): TemplateResult | typeof nothing {
    const slice = this.store.inbox
    const pending = this.store.pendingInbox().filter((i) => !this.agent || i.agentName === this.agent)
    if (slice.error && !slice.hasSnapshot) return errorState(slice.error, () => void this.store.load('inbox'))
    const stale = slice.error ? staleState(slice.error, () => void this.store.load('inbox')) : nothing
    if (!pending.length) return stale
    return html`${stale}<section class="agents-approvals">
      <h4>${icon('inbox')} Needs your attention (${pending.length})</h4>
      ${repeat(
        pending,
        (i) => i.id,
        (i) => html`<div class="agents-approval-row">
          <div class="agents-approval-body">
            <div class="agents-approval-prompt">${i.prompt}</div>
            <div class="agents-approval-meta">
              <span class="k-badge agents-badge">${i.agentName}</span>
              ${i.payload?.tool ? html`<span class="k-badge agents-badge mono">${i.payload.tool}</span>` : nothing}
              <span class="muted">${fmtTime(i.createdAt)}</span>
              ${i.runID
                ? html`<button class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'run', id: i.runID as string })}>
                    view run
                  </button>`
                : nothing}
            </div>
          </div>
          <div class="agents-approval-actions">
            ${i.kind === 'approval'
              ? html`<button class="k-btn k-btn--primary" @click=${() => void this.resolve(i, 'approve')}>${icon('check')} Approve</button>
                  <button class="k-btn k-btn--ghost secondary" @click=${() => void this.resolve(i, 'deny')}>${icon('x')} Deny</button>`
              : html`<span class="agents-hint">Answer from the agent's channel or chat.</span>`}
          </div>
        </div>`,
      )}
    </section>`
  }

  // ---- filters + table -----------------------------------------------------

  private filters(): TemplateResult {
    return html`<div class="agents-filters" role="group" aria-label="Run filters">
      ${this.agent
        ? nothing
        : html`<label class="agents-filter">
            <span class="agents-filter-label">Agent</span>
            <select class="k-input agents-filter-control" @change=${(e: Event) => {
              this.filterAgent = (e.target as HTMLSelectElement).value
              void this.reload()
            }}>
              <option value="">all</option>
              ${this.store.agents.data.map((a) => html`<option value=${a.metadata.name} ?selected=${a.metadata.name === this.filterAgent}>${a.metadata.name}</option>`)}
            </select>
          </label>`}
      <label class="agents-filter">
        <span class="agents-filter-label">Class</span>
        <select class="k-input agents-filter-control" @change=${(e: Event) => {
          this.filterClass = (e.target as HTMLSelectElement).value
          void this.reload()
        }}>
          <option value="">all</option>
          <option value="interactive" ?selected=${this.filterClass === 'interactive'}>interactive</option>
          <option value="background" ?selected=${this.filterClass === 'background'}>background</option>
        </select>
      </label>
      <label class="agents-filter">
        <span class="agents-filter-label">Phase</span>
        <select class="k-input agents-filter-control" @change=${(e: Event) => {
          this.filterPhase = (e.target as HTMLSelectElement).value
          void this.reload()
        }}>
          <option value="">all</option>
          ${PHASES.map((p) => html`<option value=${p} ?selected=${this.filterPhase === p}>${PHASE_META[p].label}</option>`)}
        </select>
      </label>
      <div class="agents-filter">
        <span class="agents-filter-label">Range</span>
        <div class="agents-seg" role="group" aria-label="Run range">
          ${RANGES.map(
            (r) => html`<button class="k-btn k-btn--ghost agents-range-button ${r.id === this.range ? 'on' : ''}"
              aria-pressed=${r.id === this.range ? 'true' : 'false'}
              @click=${() => {
                if (r.id === this.range) return
                this.range = r.id
                void this.reload()
              }}
            >
              ${r.label}
            </button>`,
          )}
        </div>
      </div>
      <button class="k-btn k-btn--ghost secondary agents-filter-refresh" aria-label="Refresh runs" @click=${() => void this.reload()}>
        ${icon('refresh')} Refresh
      </button>
    </div>`
  }

  private table(): TemplateResult {
    if (this.error && !this.loaded) return errorState(this.error, () => void this.reload('foreground'))
    if (this.loading && !this.loaded) return loadingState('Loading runs…')
    const stale = this.error ? staleState(this.error, () => void this.reload('foreground')) : nothing
    if (!this.runs.length) {
      return html`${stale}${emptyState('gauge', 'No runs yet. Chat with an agent or fire a schedule to see one here.')}`
    }
    return html`
      ${stale}
      <div class="agents-tablewrap k-table">
        <table class="agents-table agents-runs">
          <thead>
            <tr>
              ${this.agent ? nothing : html`<th>Agent</th>`}
              <th>Trigger</th>
              <th>Input</th>
              <th>Phase</th>
              <th>Duration</th>
              <th>Usage</th>
              <th>When</th>
            </tr>
          </thead>
          <tbody>
            ${repeat(
              this.runs,
              (r) => r.id,
              (r) => this.row(r),
            )}
          </tbody>
        </table>
      </div>
      ${this.nextCursor
        ? html`<div class="agents-form-actions">
            <button class="k-btn k-btn--ghost secondary" ?disabled=${this.loading} @click=${() => void this.more()}>
              ${this.loading ? 'Loading…' : 'Load more'}
            </button>
          </div>`
        : nothing}
    `
  }

  private row(r: RunSummary): TemplateResult {
    const open = (): void => this.navigate({ kind: 'run', id: r.id })
    const isNestedControl = (event: Event): boolean => {
      const currentTarget = event.currentTarget as Element | null
      const target = event.target as Element | null
      const control = target?.closest?.(
        'a, button, input, select, textarea, summary, [contenteditable="true"], [role="button"], [role="link"]',
      )
      return Boolean(control && control !== currentTarget)
    }
    return html`<tr
      class="agents-run-row"
      tabindex="0"
      aria-label="Open run ${r.id}"
      @click=${(e: Event) => {
        if (!isNestedControl(e)) open()
      }}
      @keydown=${(e: KeyboardEvent) => {
        if (isNestedControl(e)) return
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          open()
        }
      }}
    >
      ${this.agent ? nothing : html`<td><strong>${r.agent}</strong></td>`}
      <td>
        <span class="k-badge agents-badge ${r.class === 'interactive' ? 'agents-cat-tool' : ''}"
          >${icon(r.class === 'interactive' ? 'message' : 'clock')} ${r.trigger}</span
        >
        ${r.parentRunID ? html`<span class="k-badge agents-badge k-badge--muted agents-badge-muted">delegated</span>` : nothing}
      </td>
      <td class="agents-cell-task muted">${r.inputPreview || '—'}</td>
      <td>${phaseChip(r.phase)}${r.attempt && r.attempt > 1 ? html` <span class="k-badge agents-badge">try ${r.attempt}</span>` : nothing}</td>
      <td class="muted">${r.durationMS ? fmtDuration(r.durationMS) : '—'}</td>
      <td class="muted mono">
        ${fmtTokens(r.inputTokens + r.outputTokens)}${r.usdMicros ? ` · ${fmtUSD(r.usdMicros)}` : ''}
      </td>
      <td class="muted">${fmtTime(r.createdAt)}</td>
    </tr>`
  }
}

if (!customElements.get('agents-activity')) customElements.define('agents-activity', Activity)
