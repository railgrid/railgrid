// Dashboard tile for the agents provider, mounted by
// <faros-dashboard-tile-agents> (see element.ts).
//
// Agents is the one provider whose interesting state is temporal rather than
// structural: "how many agents exist" is nearly constant, while "did last
// night's run fail" and "what fires next" change constantly and are exactly
// what a user opens the console to find out. So the headline is the agent
// count, but the body is the recent run feed and the next scheduled fire.
//
// lit rather than Vue, matching this portal's stack. The shared scaffolding in
// portalkit/dashboardtile is framework-agnostic precisely so both kinds of
// portal can use it.

import { html, nothing, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { ApiClient } from '../api'
import { LightElement } from '../ui/base'
import { icon } from '../ui/icon'
import type { Agent, RunSummary, Schedule } from '../types'
import type { FarosContext } from '../types'
import {
  TILE_ROWS,
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  tileErrorText,
  type TilePoller,
} from '../portalkit/dashboardtile'

// Terminal phases that mean the run did not do its job. Anything else is
// either healthy or still in flight, and neither earns the user's attention
// from a dashboard card.
const FAILED_PHASES = new Set(['Failed', 'Error', 'Cancelled', 'Timeout'])

// Phase → dot colour, mirroring tileClass.rowDot on the Vue tiles. Unlike a
// binary ready/not-ready, a run has states worth naming (running vs succeeded
// vs cancelled), so the dot carries the TONE and the phase word stays in the
// secondary slot. That is why this row shows both where the code tile shows
// only a dot.
function phaseDot(phase: string): string {
  if (FAILED_PHASES.has(phase)) return 'agents-tile-dot-bad'
  if (phase === 'Succeeded' || phase === 'Completed') return 'agents-tile-dot-ok'
  return 'agents-tile-dot-idle'
}

export class AgentsDashboardTile extends LightElement {
  @state() private agents: Agent[] = []
  @state() private runs: RunSummary[] = []
  @state() private schedules: Schedule[] = []
  @state() private loading = true
  @state() private error: string | null = null

  private api = new ApiClient()
  private poller: TilePoller | null = null
  private _ctx: FarosContext | null = null

  set farosContext(v: FarosContext | null) {
    this._ctx = v
    this.api.setContext(v)
    this.poller?.refresh()
  }
  get farosContext(): FarosContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.poller = createTilePoller(() => this.load())
    this.poller.start()
  }

  disconnectedCallback(): void {
    this.poller?.stop()
    this.poller = null
    super.disconnectedCallback()
  }

  private async load(): Promise<void> {
    if (!hasWorkspaceContext(this._ctx)) {
      this.agents = []
      this.runs = []
      this.schedules = []
      this.error = null
      this.loading = false
      return
    }
    try {
      // One page of runs is enough: the tile shows four. Asking for the full
      // history to display a handful is how a glanceable card becomes the
      // most expensive request on the dashboard.
      const [agents, runPage, schedules] = await Promise.all([
        this.api.listAgents(),
        this.api.listRuns({ limit: TILE_ROWS * 2 } as Record<string, unknown>),
        this.api.listSchedules(),
      ])
      this.agents = agents
      this.runs = runPage.items
      this.schedules = schedules
      this.error = null
    } catch (e) {
      this.agents = []
      this.runs = []
      this.schedules = []
      this.error = isBenignTileError(e) ? null : tileErrorText(e)
    } finally {
      this.loading = false
    }
  }

  // The soonest future fire across every enabled schedule. Suspended ones are
  // excluded: they have a nextRun the platform will never act on, and showing
  // it would promise something that is not going to happen.
  private nextRun(): { at: string; agent: string } | null {
    const now = Date.now()
    const upcoming = this.schedules
      .filter((s) => !s.spec.suspend && s.status?.nextRun)
      .map((s) => ({ at: s.status!.nextRun!, agent: s.spec.agentRef }))
      .filter((s) => Date.parse(s.at) > now)
      .sort((a, b) => a.at.localeCompare(b.at))
    return upcoming[0] ?? null
  }

  private navigate(path: string): void {
    this.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
  }

  render(): TemplateResult {
    if (this.loading) return html`<div class="agents-tile-msg">Loading agents…</div>`
    if (this.error) return html`<div class="agents-tile-err">Failed to load: ${this.error}</div>`

    const failed = this.runs.filter((r) => FAILED_PHASES.has(r.phase)).length
    const next = this.nextRun()
    const rows = this.runs.slice(0, TILE_ROWS)

    return html`
      <div class="agents-tile">
        <div class="agents-tile-stats">
          <span class="agents-tile-stat"
            >${icon('bot')}<strong>${this.agents.length}</strong>
            ${this.agents.length === 1 ? 'agent' : 'agents'}</span
          >
          ${failed > 0
            ? html`<span class="agents-tile-stat agents-tile-bad"
                >${icon('alert-triangle')}<strong>${failed}</strong> failed</span
              >`
            : nothing}
          ${next
            ? html`<span class="agents-tile-stat agents-tile-next"
                >${icon('clock')}next ${relative(next.at)} · ${next.agent}</span
              >`
            : nothing}
        </div>

        ${rows.length
          ? html`<div>
              <div class="agents-tile-label">Recent runs</div>
              <ul class="agents-tile-rows">
              ${rows.map(
                (run) => html`<li>
                  <button type="button" @click=${() => this.navigate(`activity/${run.id}`)}>
                    <span class="agents-tile-dot ${phaseDot(run.phase)}" aria-hidden="true"></span>
                    <span class="agents-tile-agent">${run.agent}</span>
                    <span class=${FAILED_PHASES.has(run.phase) ? 'agents-tile-bad' : 'agents-tile-dim'}
                      >${run.phase.toLowerCase()} · ${relative(run.createdAt)}</span
                    >
                    ${chevron()}
                  </button>
                </li>`,
              )}
              </ul>
            </div>`
          : html`<p class="agents-tile-empty">
              ${this.agents.length ? 'No runs yet.' : 'No agents yet — create one to get started.'}
            </p>`}
      </div>
    `
  }
}

// chevron mirrors tileClass.chevron — the same affordance the Vue tiles use to
// signal a row is clickable.
function chevron(): TemplateResult {
  return html`<svg
    class="agents-tile-chev"
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="m9 18 6-6-6-6" />
  </svg>`
}

// relative renders a timestamp as an age (or a countdown, for a future time),
// because a raw timestamp is not something anyone subtracts at a glance.
function relative(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return 'unknown'
  const deltaSeconds = Math.round((t - Date.now()) / 1000)
  const future = deltaSeconds > 0
  const seconds = Math.abs(deltaSeconds)
  const render = (value: number, unit: string) => (future ? `in ${value}${unit}` : `${value}${unit} ago`)
  if (seconds < 60) return future ? 'in <1m' : 'just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return render(minutes, 'm')
  const hours = Math.round(minutes / 60)
  if (hours < 24) return render(hours, 'h')
  return render(Math.round(hours / 24), 'd')
}
