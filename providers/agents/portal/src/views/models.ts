// Models tab: model credentials as an ops surface, not a plain table.
//  - a usage dashboard (spend, tokens, runs, error rate, p50/p95 latency) over a
//    selectable window, with a daily spend sparkline and by-model / by-agent
//    breakdowns — all from GET /api/usage (derived from the runs table);
//  - credential cards enriched from the model catalog (pricing + capability
//    chips + context window), each with a live health probe (Test → latency +
//    served-model discovery), key rotation, edit and delete;
//  - an assignments view: which agents use each model (primary vs fallback).
//
// Each credential is its own Secret (faros-agents-model-<name>).

import { html, nothing, svg, type TemplateResult } from 'lit'
import { state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState } from '../ui/states'
import { toast } from '../ui/toast'
import { confirmModal } from '../portalkit/modal'
import { mutate } from '../mutate'
import { PROVIDER_PRESETS } from '../conn-defs'
import {
  fmtTokens,
  fmtUSD,
  type Credential,
  type CredentialTestResult,
  type ModelInfo,
  type UsagePoint,
  type UsageResponse,
} from '../types'

export class Models extends StoreElement {
  @state() private catalog: ModelInfo[] = []
  @state() private usage: UsageResponse | null = null
  @state() private usageError: string | null = null
  @state() private windowDays = 30
  @state() private tested = new Map<string, CredentialTestResult>()
  @state() private discovered = new Map<string, string[]>()
  @state() private discFilter = new Map<string, string>()
  @state() private editName: string | null = null
  @state() private creating = false

  private started = false

  protected willUpdate(): void {
    super.willUpdate()
    if (this.started || !this.api) return
    this.started = true
    void this.api.catalog().then(
      (c) => (this.catalog = c),
      () => (this.catalog = []),
    )
    void this.loadUsage()
  }

  private async loadUsage(): Promise<void> {
    try {
      this.usage = await this.api.usage(this.windowDays)
      this.usageError = null
    } catch (e) {
      this.usage = null
      this.usageError = (e as Error).message
    }
  }

  // lookupModel mirrors the backend's catalog normalization: exact id first,
  // then the longest prefix match (so "gpt-4o-2024-08-06" finds "gpt-4o").
  private lookupModel(model: string): ModelInfo | undefined {
    const norm = (model || '').toLowerCase().trim().replace(/^.*\//, '')
    if (!norm) return undefined
    let exact: ModelInfo | undefined
    let best: ModelInfo | undefined
    for (const m of this.catalog) {
      if (m.id === norm) exact = m
      if (norm.startsWith(m.id) && (!best || m.id.length > best.id.length)) best = m
    }
    return exact || best
  }

  private async testCredential(name: string): Promise<void> {
    try {
      const res = await this.api.testCredential(name)
      this.tested = new Map(this.tested).set(name, res)
      if (res.models?.length) this.discovered = new Map(this.discovered).set(name, res.models)
      toast(
        res.ok ? 'ok' : 'error',
        res.ok ? `${name}: healthy · ${res.latencyMS}ms${res.models?.length ? ` · ${res.models.length} models` : ''}` : `${name}: ${res.error || 'failed'}`,
      )
    } catch (e) {
      this.tested = new Map(this.tested).set(name, { ok: false, latencyMS: 0, error: (e as Error).message })
      toast('error', `${name}: ${(e as Error).message}`)
    }
  }

  private async del(name: string): Promise<void> {
    const ok = await confirmModal({
      title: `Delete credential “${name}”?`,
      message: 'Agents using it will need reassigning.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok) return
    await mutate(this.store, {
      run: () => this.api.deleteCredential(name),
      success: 'Credential deleted.',
      failure: 'Delete failed',
      reload: ['credentials'],
    })
  }

  render(): TemplateResult {
    const creds = this.store.credentials
    return html`<div class="agents-panel k-card agents-route-panel">
      <div class="agents-panel-head">
        <h3>Models</h3>
        ${this.creating ? nothing : html`<button class="k-btn k-btn--primary" @click=${() => (this.creating = true)}>${icon('plus')} New model</button>`}
      </div>
      <p class="muted">
        Model credentials shared across the workspace (each is a Secret <code>faros-agents-model-&lt;name&gt;</code>). Assign them to
        agents in each agent's Config pane.
      </p>
      ${this.dashboard()}
      <h3 class="agents-section-h">Credentials</h3>
      ${creds.error
        ? errorState(creds.error, () => void this.store.load('credentials'))
        : creds.data.length === 0
          ? html`<p class="agents-hint">${icon('cpu')} No models yet${this.creating ? '.' : ' — add one below.'}</p>`
          : html`<div class="agents-model-grid">
              ${repeat(
                creds.data,
                (c) => c.name,
                (c) => this.card(c),
              )}
            </div>`}
      ${this.creating ? this.createForm() : nothing}
      <datalist id="agents-catalog-models">
        ${this.catalog.map((m) => html`<option value=${m.id}>${m.label || m.id}</option>`)}
      </datalist>
    </div>`
  }

  // ---- dashboard -----------------------------------------------------------

  private dashboard(): TemplateResult {
    if (this.usageError) return errorState(`Usage unavailable: ${this.usageError}`, () => void this.loadUsage())
    if (!this.usage) return html`<div class="k-card agents-dash-loading muted" role="status">Loading usage…</div>`
    // Normalize defensively as well as in the client: a throw in here takes the
    // whole Models view down with it, including the controls to fix whatever
    // was wrong. Nothing about an empty workspace should cost the user the page.
    const u = {
      ...this.usage,
      byAgent: this.usage.byAgent ?? [],
      byModel: this.usage.byModel ?? [],
      series: this.usage.series ?? [],
    }
    const t = u.total
    const tokens = t.inputTokens + t.outputTokens
    const errRate = t.runs ? Math.round((t.errors / t.runs) * 100) + '%' : '0%'
    const stat = (label: string, value: string, sub = ''): TemplateResult => html`<div class="k-card agents-stat">
      <div class="agents-stat-v">${value}</div>
      <div class="agents-stat-k">${label}</div>
      ${sub ? html`<div class="agents-stat-sub">${sub}</div>` : nothing}
    </div>`
    const maxModel = Math.max(1, ...u.byModel.map((b) => b.usdMicros))
    const maxAgent = Math.max(1, ...u.byAgent.map((b) => b.usdMicros))
    return html`<div class="k-card agents-dash">
      <div class="agents-dash-head">
        <h3>Usage &amp; cost</h3>
        <div class="agents-seg" role="group" aria-label="Usage window">
          ${[7, 30, 90].map(
            (d) => html`<button class="k-btn k-btn--ghost ${d === this.windowDays ? 'on' : ''}"
              aria-pressed=${d === this.windowDays ? 'true' : 'false'}
              @click=${() => {
                if (d === this.windowDays) return
                this.windowDays = d
                this.usage = null
                void this.loadUsage()
              }}
            >
              ${d}d
            </button>`,
          )}
        </div>
      </div>
      <div class="agents-stats">
        ${stat('spend', fmtUSD(t.usdMicros), `${u.windowDays}d`)}
        ${stat('tokens', fmtTokens(tokens), `${fmtTokens(t.inputTokens)} in · ${fmtTokens(t.outputTokens)} out`)}
        ${stat('runs', String(t.runs), `${errRate} errors`)}
        ${stat('latency', t.latencyP50MS ? `${t.latencyP50MS}ms` : '—', t.latencyP95MS ? `${t.latencyP95MS}ms p95` : 'p50 / p95')}
      </div>
      <div class="agents-dash-grid">
        <div class="k-card agents-dash-card">
          <div class="agents-dash-card-h">Daily spend</div>
          ${sparkline(u.series)}
        </div>
        <div class="k-card agents-dash-card">
          <div class="agents-dash-card-h">Spend by model</div>
          <div class="agents-bars">
            ${u.byModel.length && u.byModel.some((b) => b.usdMicros > 0 || b.runs > 0)
              ? u.byModel.slice(0, 6).map((b) => bar(b.key, b.usdMicros, maxModel, `${fmtUSD(b.usdMicros)} · ${b.runs} run${b.runs === 1 ? '' : 's'}`))
              : html`<div class="muted agents-bars-empty">No runs yet in this window.</div>`}
          </div>
        </div>
        <div class="k-card agents-dash-card">
          <div class="agents-dash-card-h">Spend by agent</div>
          <div class="agents-bars">
            ${u.byAgent.length
              ? u.byAgent
                  .slice(0, 6)
                  .map((b) =>
                    bar(b.key, b.usdMicros, maxAgent, `${fmtUSD(b.usdMicros)} · ${b.latencyP50MS ? b.latencyP50MS + 'ms p50' : '—'}${b.errors ? ` · ${b.errors} err` : ''}`),
                  )
              : html`<div class="muted agents-bars-empty">—</div>`}
          </div>
        </div>
      </div>
    </div>`
  }

  // ---- credential cards ----------------------------------------------------

  private card(c: Credential): TemplateResult {
    const mi = this.lookupModel(c.model || '')
    // Assignments: which agents reason with this credential, and in what role.
    const primaryOf = this.store.agents.data.filter((a) => a.spec?.models?.chat === c.name)
    const fallbackOf = this.store.agents.data.filter((a) => a.spec?.models?.chat !== c.name && (a.spec?.modelFallbacks || []).includes(c.name))
    const disc = this.discovered.get(c.name)
    const isEditing = this.editName === c.name
    return html`<article class="k-card agents-model-card ${isEditing ? 'is-editing' : ''}">
      <div class="agents-model-head">
        <div class="agents-model-title">
          <span class="agents-model-glyph">${icon('cpu')}</span>
          <div>
            <h4>${c.name}</h4>
            <div class="agents-model-sub">
              <span class="mono">${c.model || '—'}</span>${mi?.label ? ` · ${mi.label}` : ''}
            </div>
          </div>
        </div>
        ${this.healthBadge(c.name)}
      </div>
      <div class="agents-model-chips">
        ${mi ? capabilityChips(mi) : html`<span class="agents-chip agents-chip-warn">not in catalog — no pricing</span>`}
      </div>
      <div class="agents-model-meta">
        <span class="muted">${c.provider || 'openai-compatible'}</span>
        ${c.baseURL ? html`<span class="muted mono">${c.baseURL}</span>` : nothing}
      </div>
      <div class="agents-model-assign">
        ${primaryOf.length || fallbackOf.length
          ? html`${primaryOf.map(
                (a) => html`<span class="agents-chip agents-chip-primary" title="primary model"
                  >${icon('chevron-right')} ${a.spec?.displayName || a.metadata.name}</span
                >`,
              )}
              ${fallbackOf.map(
                (a) => html`<span class="agents-chip agents-chip-fallback" title="fallback model"
                  >${icon('corner-down-right')} ${a.spec?.displayName || a.metadata.name}</span
                >`,
              )}`
          : html`<span class="muted agents-assign-none">not assigned to any agent</span>`}
      </div>
      ${disc?.length ? this.servedModels(c, disc) : nothing}
      ${isEditing ? this.rotateForm(c) : nothing}
      <div class="agents-model-actions">
        <button class="k-btn k-btn--ghost secondary" @click=${() => void this.testCredential(c.name)}>${icon('plug')} Test</button>
        <button class="k-btn k-btn--ghost secondary" @click=${() => (this.editName = isEditing ? null : c.name)}>
          ${isEditing ? 'Close' : html`${icon('key')} Rotate / model`}
        </button>
        <button class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger" aria-label="Delete ${c.name}" title="Delete" @click=${() => void this.del(c.name)}>
          ${icon('trash')}
        </button>
      </div>
    </article>`
  }

  private healthBadge(name: string): TemplateResult {
    const t = this.tested.get(name)
    if (!t) return html`<span class="k-badge k-badge--muted agents-health agents-health-unknown" title="not tested">${icon('circle')} untested</span>`
    if (t.ok) return html`<span class="k-badge k-badge--success agents-health agents-health-ok" title="healthy">${icon('circle')} healthy · ${t.latencyMS}ms</span>`
    return html`<span class="k-badge k-badge--danger agents-health agents-health-bad" title=${t.error || 'failed'}>${icon('circle')} failed</span>`
  }

  // servedModels renders the endpoint's discovered model list as a filterable
  // picker. Endpoints routinely serve dozens-to-hundreds of ids (OpenRouter is
  // 300+), so a silently capped chip dump hides exactly the model the user is
  // hunting for: filter first, cap what's visible, and always say what's hidden.
  private servedModels(c: Credential, disc: string[]): TemplateResult {
    const raw = this.discFilter.get(c.name) || ''
    const filter = raw.toLowerCase().trim()
    const matches = filter ? disc.filter((m) => m.toLowerCase().includes(filter)) : disc
    const visible = matches.slice(0, 30)
    return html`<div class="agents-model-discovered">
      <div class="agents-discovered-head">
        <span class="muted">${disc.length} served model${disc.length === 1 ? '' : 's'} — click to switch:</span>
        ${disc.length > 12
          ? html`<input class="k-input agents-discovered-filter mono"
              placeholder="filter…"
              .value=${raw}
              @input=${(e: Event) => (this.discFilter = new Map(this.discFilter).set(c.name, (e.target as HTMLInputElement).value))}
            />`
          : nothing}
      </div>
      ${visible.map(
        (m) =>
          html`<button
            class="k-btn k-btn--ghost agents-chip agents-chip-btn ${m === c.model ? 'agents-chip-current' : ''}"
            title=${m === c.model ? 'current model' : `switch ${c.name} to ${m}`}
            @click=${() => void this.switchModel(c, m)}
          >
            ${m}
          </button>`,
      )}
      ${matches.length > visible.length
        ? html`<span class="agents-hint">+${matches.length - visible.length} more — refine the filter to see them</span>`
        : nothing}
      ${matches.length === 0 ? html`<span class="agents-hint">nothing matches “${raw}”</span>` : nothing}
    </div>`
  }

  private async switchModel(c: Credential, model: string): Promise<void> {
    // The credential POST is an upsert: re-posting name+baseURL with a new
    // model keeps the stored API key.
    const res = await mutate(this.store, {
      run: () => this.api.saveCredential({ name: c.name, provider: c.provider || 'openai-compatible', baseURL: c.baseURL, model }),
      success: `${c.name} now uses ${model}.`,
      failure: 'Save failed',
      reload: ['credentials'],
    })
    if (res) {
      const next = new Map(this.tested)
      next.delete(c.name)
      this.tested = next
    }
  }

  private rotateForm(c: Credential): TemplateResult {
    // The model input starts empty (current model as placeholder) instead of
    // pre-filled: browsers filter datalist suggestions by the input's value, so
    // a pre-filled id hides every other option — the exact "can't see the list"
    // trap this form exists to avoid. Blank means keep, same as the key field.
    // Suggestions merge what the endpoint actually serves (from Test) with the
    // curated catalog, served ids first.
    const served = this.discovered.get(c.name) || []
    const servedSet = new Set(served)
    const listId = `agents-models-${c.name}`
    return html`<form
      class="agents-rotate-form k-card"
      @submit=${(e: Event) => {
        e.preventDefault()
        const f = e.target as HTMLFormElement
        const g = (n: string): string => (f.querySelector<HTMLInputElement>(`[name=${n}]`)?.value || '').trim()
        const key = g('apiKey')
        void mutate(this.store, {
          run: () =>
            this.api.saveCredential({
              name: c.name,
              provider: c.provider || 'openai-compatible',
              model: g('model') || c.model || '',
              baseURL: g('baseURL'),
              ...(key ? { apiKey: key } : {}),
            }),
          success: 'Credential saved.',
          failure: 'Save failed',
          reload: ['credentials'],
        }).then((ok) => {
          if (!ok) return
          this.editName = null
          const next = new Map(this.tested)
          next.delete(c.name)
          this.tested = next
        })
      }}
    >
      <div class="agents-grid2">
        <label>
          Model <span class="agents-hint">leave blank to keep ${c.model || 'the current one'}</span>
          <input class="k-input mono" name="model"  placeholder=${c.model || 'gpt-4o'} list=${listId} />
        </label>
        <label>Base URL<input class="k-input mono" name="baseURL" .value=${c.baseURL || ''}  placeholder="https://api.openai.com/v1" /></label>
      </div>
      <datalist id=${listId}>
        ${served.map((m) => html`<option value=${m}></option>`)}
        ${this.catalog.filter((m) => !servedSet.has(m.id)).map((m) => html`<option value=${m.id}>${m.label || m.id}</option>`)}
      </datalist>
      <label>
        New API key <span class="agents-hint">leave blank to keep the current key</span>
        <input class="k-input" name="apiKey" type="password" autocomplete="off" placeholder="sk-… (rotate)" />
      </label>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="submit">Save</button>
        <button type="button" class="k-btn k-btn--ghost secondary" @click=${() => (this.editName = null)}>Cancel</button>
      </div>
    </form>`
  }

  private createForm(): TemplateResult {
    return html`<form
      class="agents-cred-form agents-model-create"
      @submit=${(e: Event) => {
        e.preventDefault()
        const f = e.target as HTMLFormElement
        const g = (n: string): string => (f.querySelector<HTMLInputElement>(`[name=${n}]`)?.value || '').trim()
        void mutate(this.store, {
          run: () =>
            this.api.saveCredential({
              name: g('name'),
              provider: 'openai-compatible',
              baseURL: g('baseURL'),
              model: g('model'),
              apiKey: g('apiKey'),
            }),
          success: 'Credential saved.',
          failure: 'Save failed',
          reload: ['credentials'],
        }).then((ok) => ok && (this.creating = false))
      }}
    >
      <h4>New model credential</h4>
      <div class="agents-grid2">
        <label>Name<input class="k-input" name="name" required pattern="[a-z0-9-]+" placeholder="my-openai" /></label>
        <label>
          Provider
          <select class="k-input"
            name="preset"
            @change=${(e: Event) => {
              const p = PROVIDER_PRESETS.find((x) => x.id === (e.target as HTMLSelectElement).value)
              if (!p) return
              const form = (e.target as HTMLElement).closest('form')!
              const baseURL = form.querySelector<HTMLInputElement>('input[name=baseURL]')!
              const model = form.querySelector<HTMLInputElement>('input[name=model]')!
              if (p.id !== 'custom') baseURL.value = p.baseURL
              model.placeholder = p.modelHint
            }}
          >
            ${PROVIDER_PRESETS.map((p) => html`<option value=${p.id}>${p.label}</option>`)}
          </select>
        </label>
        <label>Base URL<input class="k-input mono" name="baseURL"  .value=${PROVIDER_PRESETS[0].baseURL} placeholder="https://api.openai.com/v1" /></label>
        <label>Model<input class="k-input mono" name="model"  placeholder="gpt-4o" required list="agents-catalog-models" /></label>
      </div>
      <label>API key<input class="k-input" name="apiKey" type="password" autocomplete="off" placeholder="sk-…" required /></label>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary" type="submit">Add credential</button>
        <button type="button" class="k-btn k-btn--ghost secondary" @click=${() => (this.creating = false)}>Cancel</button>
      </div>
    </form>`
  }
}

// capabilityChips renders capability + pricing badges for a catalog entry.
function capabilityChips(mi: ModelInfo): TemplateResult {
  return html`
    ${mi.contextWindow ? html`<span class="agents-chip">${fmtCtx(mi.contextWindow)}</span>` : nothing}
    ${mi.vision ? html`<span class="agents-chip">${icon('eye')} vision</span>` : nothing}
    ${mi.toolCall ? html`<span class="agents-chip">${icon('wrench')} tools</span>` : nothing}
    ${mi.reasoning ? html`<span class="agents-chip">${icon('brain')} reasoning</span>` : nothing}
    <span class="agents-chip agents-chip-price">$${mi.inputPer1M}/$${mi.outputPer1M} per 1M</span>
  `
}

function fmtCtx(n: number): string {
  if (n >= 1e6) return n / 1e6 + 'M ctx'
  if (n >= 1e3) return Math.round(n / 1e3) + 'k ctx'
  return n + ' ctx'
}

// sparkline draws a tiny inline-SVG area chart of daily spend (monochrome,
// theme-aware via the accent token). No chart lib — the bundle stays
// self-contained.
function sparkline(series: UsagePoint[]): TemplateResult {
  const pts = series.map((p) => p.usdMicros)
  if (pts.length < 2 || Math.max(...pts) === 0) return html`<div class="agents-spark-empty muted">no spend in this window</div>`
  const w = 260
  const h = 40
  const max = Math.max(...pts)
  const step = w / (pts.length - 1)
  const line = pts.map((v, i) => `${(i * step).toFixed(1)},${(h - (v / max) * (h - 4) - 2).toFixed(1)}`).join(' ')
  return html`<svg class="agents-spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" role="img" aria-label="daily spend">
    ${svg`<polygon class="agents-spark-fill" points="0,${h} ${line} ${w},${h}" />
    <polyline class="agents-spark-line" points="${line}" />`}
  </svg>`
}

// bar renders one labeled horizontal bar (value relative to max).
function bar(label: string, value: number, max: number, right: string): TemplateResult {
  const pct = max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0
  return html`<div class="agents-bar-row">
    <div class="agents-bar-label" title=${label}>${label}</div>
    <div class="agents-bar-track"><div class="agents-bar-fill" style="width:${pct}%"></div></div>
    <div class="agents-bar-val">${right}</div>
  </div>`
}

if (!customElements.get('agents-models')) customElements.define('agents-models', Models)
