// Models menu: model credentials as an ops surface, not a plain table.
//  - a usage dashboard (spend, tokens, runs, error rate, p50/p95 latency) over a
//    selectable window, with a daily spend sparkline and by-model / by-agent
//    breakdowns — all from GET /api/usage (derived from the runs table);
//  - credential cards enriched from the model catalog (pricing + capability
//    chips + context window), each with a live health probe (Test → latency +
//    served-model discovery), key rotation, edit and delete;
//  - an assignments view: which agents use each model (primary vs fallback).
//
// Each credential is its own Secret (kedge-agents-model-<name>).

import { ic } from '../icons'
import type { ViewCtx } from '../view'
import type { Credential } from '../types'
import { escapeHTML } from '../types'
import { PROVIDER_PRESETS } from '../conn-defs'
import { createCredential, deleteCredential } from '../actions'

// ---- data shapes (mirror the backend JSON) ---------------------------------

interface ModelInfo {
  id: string
  family: string
  label?: string
  contextWindow?: number
  inputPer1M: number
  outputPer1M: number
  vision?: boolean
  toolCall?: boolean
  reasoning?: boolean
}
interface UsageBucket {
  key: string
  runs: number
  errors: number
  inputTokens: number
  outputTokens: number
  usdMicros: number
  latencyP50MS: number
  latencyP95MS: number
}
interface UsagePoint {
  date: string
  runs: number
  inputTokens: number
  outputTokens: number
  usdMicros: number
}
interface UsageResponse {
  windowDays: number
  total: UsageBucket
  byAgent: UsageBucket[]
  byModel: UsageBucket[]
  series: UsagePoint[]
}
interface TestResult {
  ok: boolean
  latencyMS: number
  error?: string
  models?: string[]
}

// ---- view-local state ------------------------------------------------------

let catalog: ModelInfo[] = []
let catalogLoaded = false
let usage: UsageResponse | null = null
let windowDays = 30
const tested = new Map<string, TestResult>() // credential name → last probe
const discovered = new Map<string, string[]>() // credential name → served model ids
let editName: string | null = null // credential being edited (rotate/model), or null
let creating = false
let msg: string | null = null

// resetForTenant clears cross-workspace state on a tenant switch.
export function resetForTenant(): void {
  catalog = []
  catalogLoaded = false
  usage = null
  tested.clear()
  discovered.clear()
  editName = null
  creating = false
  msg = null
}

async function ensureLoaded(vc: ViewCtx): Promise<void> {
  if (!catalogLoaded) {
    catalogLoaded = true
    try {
      catalog = await vc.api.list<ModelInfo>('/api/catalog')
    } catch {
      catalog = []
    }
    vc.rerender()
  }
  if (!usage) void loadUsage(vc)
}

async function loadUsage(vc: ViewCtx): Promise<void> {
  try {
    usage = await vc.api.get<UsageResponse>(`/api/usage?days=${windowDays}`)
  } catch {
    usage = null
  }
  vc.rerender()
}

// ---- catalog helpers (client mirror of the backend normalization) ----------

function lookupModel(model: string): ModelInfo | undefined {
  const norm = (model || '').toLowerCase().trim().replace(/^.*\//, '')
  if (!norm) return undefined
  let exact: ModelInfo | undefined
  let best: ModelInfo | undefined
  for (const m of catalog) {
    if (m.id === norm) exact = m
    if (norm.startsWith(m.id) && (!best || m.id.length > best.id.length)) best = m
  }
  return exact || best
}

// ---- formatting ------------------------------------------------------------

function fmtUSD(micros: number): string {
  const usd = micros / 1e6
  if (usd === 0) return '$0'
  if (usd < 0.01) return '$' + usd.toFixed(4)
  if (usd < 100) return '$' + usd.toFixed(2)
  return '$' + Math.round(usd).toLocaleString()
}
function fmtTokens(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return String(n)
}
function fmtCtx(n?: number): string {
  if (!n) return ''
  if (n >= 1e6) return n / 1e6 + 'M ctx'
  if (n >= 1e3) return Math.round(n / 1e3) + 'k ctx'
  return n + ' ctx'
}
function pct(n: number, d: number): string {
  if (d === 0) return '0%'
  return Math.round((n / d) * 100) + '%'
}

// capabilityChips renders capability + pricing badges for a catalog entry.
function capabilityChips(mi: ModelInfo): string {
  const chips: string[] = []
  if (mi.contextWindow) chips.push(`<span class="agents-chip">${fmtCtx(mi.contextWindow)}</span>`)
  if (mi.vision) chips.push(`<span class="agents-chip">${ic('eye')} vision</span>`)
  if (mi.toolCall) chips.push(`<span class="agents-chip">${ic('wrench')} tools</span>`)
  if (mi.reasoning) chips.push(`<span class="agents-chip">${ic('brain')} reasoning</span>`)
  chips.push(`<span class="agents-chip agents-chip-price">$${mi.inputPer1M}/$${mi.outputPer1M} per 1M</span>`)
  return chips.join('')
}

// sparkline draws a tiny inline-SVG area chart of daily spend (monochrome,
// theme-aware via the accent token). No chart lib — the page is self-contained.
function sparkline(series: UsagePoint[]): string {
  const pts = series.map((p) => p.usdMicros)
  if (pts.length < 2 || Math.max(...pts) === 0) return '<div class="agents-spark-empty muted">no spend in this window</div>'
  const w = 260
  const h = 40
  const max = Math.max(...pts)
  const step = w / (pts.length - 1)
  const coords = pts.map((v, i) => `${(i * step).toFixed(1)},${(h - (v / max) * (h - 4) - 2).toFixed(1)}`)
  const line = coords.join(' ')
  const area = `0,${h} ${line} ${w},${h}`
  return `<svg class="agents-spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" role="img" aria-label="daily spend">
      <polygon class="agents-spark-fill" points="${area}" />
      <polyline class="agents-spark-line" points="${line}" />
    </svg>`
}

// bar renders one labeled horizontal bar (value relative to max).
function bar(label: string, value: number, max: number, right: string): string {
  const w = max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0
  return `<div class="agents-bar-row">
      <div class="agents-bar-label" title="${escapeHTML(label)}">${escapeHTML(label)}</div>
      <div class="agents-bar-track"><div class="agents-bar-fill" style="width:${w}%"></div></div>
      <div class="agents-bar-val">${escapeHTML(right)}</div>
    </div>`
}

// ---- render ----------------------------------------------------------------

function renderDashboard(): string {
  if (!usage) return `<div class="agents-dash-loading muted">Loading usage…</div>`
  const t = usage.total
  const errRate = pct(t.errors, t.runs)
  const tokens = t.inputTokens + t.outputTokens
  const stat = (label: string, value: string, sub = '') =>
    `<div class="agents-stat"><div class="agents-stat-v">${value}</div><div class="agents-stat-k">${escapeHTML(label)}</div>${sub ? `<div class="agents-stat-sub">${escapeHTML(sub)}</div>` : ''}</div>`
  const maxModel = Math.max(1, ...usage.byModel.map((b) => b.usdMicros))
  const modelBars =
    usage.byModel.length && usage.byModel.some((b) => b.usdMicros > 0 || b.runs > 0)
      ? usage.byModel
          .slice(0, 6)
          .map((b) => bar(b.key, b.usdMicros, maxModel, `${fmtUSD(b.usdMicros)} · ${b.runs} run${b.runs === 1 ? '' : 's'}`))
          .join('')
      : '<div class="muted" style="font-size:12px">No runs yet in this window.</div>'
  const maxAgent = Math.max(1, ...usage.byAgent.map((b) => b.usdMicros))
  const agentBars = usage.byAgent
    .slice(0, 6)
    .map((b) => bar(b.key, b.usdMicros, maxAgent, `${fmtUSD(b.usdMicros)} · ${b.latencyP50MS ? b.latencyP50MS + 'ms p50' : '—'}${b.errors ? ` · ${b.errors} err` : ''}`))
    .join('')
  return `
    <div class="agents-dash">
      <div class="agents-dash-head">
        <h3>Usage &amp; cost</h3>
        <div class="agents-seg" data-window>
          ${[7, 30, 90].map((d) => `<button class="${d === windowDays ? 'on' : ''}" data-days="${d}">${d}d</button>`).join('')}
        </div>
      </div>
      <div class="agents-stats">
        ${stat('spend', fmtUSD(t.usdMicros), `${usage.windowDays}d`)}
        ${stat('tokens', fmtTokens(tokens), `${fmtTokens(t.inputTokens)} in · ${fmtTokens(t.outputTokens)} out`)}
        ${stat('runs', String(t.runs), errRate + ' errors')}
        ${stat('latency', t.latencyP50MS ? t.latencyP50MS + 'ms' : '—', t.latencyP95MS ? t.latencyP95MS + 'ms p95' : 'p50 / p95')}
      </div>
      <div class="agents-dash-grid">
        <div class="agents-dash-card">
          <div class="agents-dash-card-h">Daily spend</div>
          ${sparkline(usage.series)}
        </div>
        <div class="agents-dash-card">
          <div class="agents-dash-card-h">Spend by model</div>
          <div class="agents-bars">${modelBars}</div>
        </div>
        <div class="agents-dash-card">
          <div class="agents-dash-card-h">Spend by agent</div>
          <div class="agents-bars">${agentBars || '<div class="muted" style="font-size:12px">—</div>'}</div>
        </div>
      </div>
    </div>`
}

function healthBadge(name: string): string {
  const t = tested.get(name)
  if (!t) return `<span class="agents-health agents-health-unknown" title="not tested">${ic('circle')} untested</span>`
  if (t.ok) return `<span class="agents-health agents-health-ok" title="healthy">${ic('circle')} healthy · ${t.latencyMS}ms</span>`
  return `<span class="agents-health agents-health-bad" title="${escapeHTML(t.error || 'failed')}">${ic('circle')} failed</span>`
}

function credentialCard(vc: ViewCtx, c: Credential): string {
  const mi = lookupModel(c.model || '')
  // Assignments: which agents reason with this credential, and in what role.
  // primary = spec.models.chat; fallback = appears in spec.modelFallbacks. This
  // surfaces the existing fallback/routing chain in the Models context.
  const primaryOf = vc.store.agents.filter((a) => a.spec?.models?.chat === c.name)
  const fallbackOf = vc.store.agents.filter((a) => a.spec?.models?.chat !== c.name && (a.spec?.modelFallbacks || []).includes(c.name))
  const usedBy = [...primaryOf, ...fallbackOf]
  const disc = discovered.get(c.name)
  const isEditing = editName === c.name
  const chips = mi ? capabilityChips(mi) : `<span class="agents-chip agents-chip-warn">not in catalog — no pricing</span>`
  return `
    <article class="agents-model-card ${isEditing ? 'is-editing' : ''}">
      <div class="agents-model-head">
        <div class="agents-model-title">
          <span class="agents-model-glyph">${ic('settings')}</span>
          <div>
            <h4>${escapeHTML(c.name)}</h4>
            <div class="agents-model-sub"><span class="mono">${escapeHTML(c.model || '—')}</span>${mi?.label ? ` · ${escapeHTML(mi.label)}` : ''}</div>
          </div>
        </div>
        ${healthBadge(c.name)}
      </div>
      <div class="agents-model-chips">${chips}</div>
      <div class="agents-model-meta">
        <span class="muted">${escapeHTML(c.provider || 'openai-compatible')}</span>
        ${c.baseURL ? `<span class="muted mono">${escapeHTML(c.baseURL)}</span>` : ''}
      </div>
      <div class="agents-model-assign">
        ${
          usedBy.length
            ? [
                ...primaryOf.map((a) => `<span class="agents-chip agents-chip-primary" title="primary model">${ic('chevron-right')} ${escapeHTML(a.spec?.displayName || a.metadata.name)}</span>`),
                ...fallbackOf.map((a) => `<span class="agents-chip agents-chip-fallback" title="fallback model">${ic('corner-down-right')} ${escapeHTML(a.spec?.displayName || a.metadata.name)}</span>`),
              ].join('')
            : '<span class="muted" style="font-size:12px">not assigned to any agent</span>'
        }
      </div>
      ${
        disc && disc.length
          ? `<div class="agents-model-discovered"><span class="muted">served models — click to switch:</span> ${disc
              .slice(0, 24)
              .map((m) => `<button class="agents-chip agents-chip-btn" data-setmodel="${escapeHTML(c.name)}" data-model="${escapeHTML(m)}">${escapeHTML(m)}</button>`)
              .join('')}</div>`
          : ''
      }
      ${isEditing ? renderRotate(c) : ''}
      <div class="agents-model-actions">
        <button class="secondary" data-testcred="${escapeHTML(c.name)}">${ic('plug')} Test</button>
        <button class="secondary" data-editcred="${escapeHTML(c.name)}">${isEditing ? 'Close' : `${ic('key')} Rotate / model`}</button>
        <button class="agents-iconbtn agents-iconbtn-danger" data-delcred="${escapeHTML(c.name)}" title="Delete">${ic('trash')}</button>
      </div>
    </article>`
}

// renderRotate is the inline edit panel: rotate the key and/or change the model.
function renderRotate(c: Credential): string {
  return `<form class="agents-rotate-form" data-rotate="${escapeHTML(c.name)}">
      <div class="agents-grid2">
        <label>Model<input name="model" value="${escapeHTML(c.model || '')}" class="mono" placeholder="gpt-4o" list="agents-catalog-models" /></label>
        <label>Base URL<input name="baseURL" value="${escapeHTML(c.baseURL || '')}" class="mono" placeholder="https://api.openai.com/v1" /></label>
      </div>
      <label>New API key <span class="agents-hint">leave blank to keep the current key</span><input name="apiKey" type="password" autocomplete="off" placeholder="sk-… (rotate)" /></label>
      <div class="agents-form-actions"><button>Save</button><button type="button" class="secondary" data-editcancel>Cancel</button></div>
    </form>`
}

export function render(vc: ViewCtx): string {
  void ensureLoaded(vc)
  const creds = vc.store.credentials
  const cards = creds.length
    ? creds.map((c) => credentialCard(vc, c)).join('')
    : `<div class="agents-empty-row"><span class="agents-empty">${ic('settings')} No models yet — add one below.</span></div>`
  const createForm = creating
    ? `<form class="agents-cred-form agents-model-create">
        <h4>New model credential</h4>
        <div class="agents-grid2">
          <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="my-openai" /></label>
          <label>Provider<select name="preset">${PROVIDER_PRESETS.map((p) => `<option value="${p.id}">${escapeHTML(p.label)}</option>`).join('')}</select></label>
          <label>Base URL<input name="baseURL" class="mono" value="${PROVIDER_PRESETS[0].baseURL}" placeholder="https://api.openai.com/v1" /></label>
          <label>Model<input name="model" class="mono" placeholder="gpt-4o" required list="agents-catalog-models" /></label>
        </div>
        <label>API key<input name="apiKey" type="password" autocomplete="off" placeholder="sk-…" required /></label>
        ${msg ? `<div class="agents-msg-note">${escapeHTML(msg)}</div>` : ''}
        <div class="agents-form-actions"><button>Add credential</button><button type="button" class="secondary" data-createcancel>Cancel</button></div>
      </form>`
    : ''
  // A datalist of known catalog model ids helps autocomplete the model field.
  const datalist = `<datalist id="agents-catalog-models">${catalog.map((m) => `<option value="${escapeHTML(m.id)}">${escapeHTML(m.label || m.id)}</option>`).join('')}</datalist>`
  return `
    <div class="agents-panel">
      <div class="agents-panel-head"><h3>Models</h3>${creating ? '' : `<button data-newcred>${ic('plus')} New model</button>`}</div>
      <p class="muted">Model credentials shared across the workspace (each is a Secret <code>kedge-agents-model-&lt;name&gt;</code>). Assign them to agents in each agent's Settings or Flow.</p>
      ${renderDashboard()}
      <h3 class="agents-section-h">Credentials</h3>
      <div class="agents-model-grid">${cards}</div>
      ${createForm}
      ${datalist}
    </div>`
}

// ---- wire ------------------------------------------------------------------

async function testCredential(vc: ViewCtx, name: string): Promise<void> {
  vc.notify(`Testing ${name}…`)
  try {
    const res = await vc.api.send<TestResult>('POST', `/api/credentials/${encodeURIComponent(name)}/test`)
    tested.set(name, res)
    if (res.models?.length) discovered.set(name, res.models)
    vc.notify(res.ok ? `${name}: healthy · ${res.latencyMS}ms${res.models?.length ? ` · ${res.models.length} models` : ''}` : `${name}: ${res.error || 'failed'}`)
  } catch (e) {
    tested.set(name, { ok: false, latencyMS: 0, error: (e as Error).message })
    vc.notify(`${name}: ${(e as Error).message}`)
  }
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  // Window selector for the dashboard.
  root.querySelectorAll<HTMLElement>('[data-window] button').forEach((el) =>
    el.addEventListener('click', () => {
      const d = Number(el.dataset.days)
      if (d && d !== windowDays) {
        windowDays = d
        usage = null
        vc.rerender()
        void loadUsage(vc)
      }
    }),
  )
  root.querySelector<HTMLElement>('[data-newcred]')?.addEventListener('click', () => {
    creating = true
    msg = null
    vc.rerender()
  })
  root.querySelector<HTMLElement>('[data-createcancel]')?.addEventListener('click', () => {
    creating = false
    vc.rerender()
  })
  root.querySelectorAll<HTMLElement>('[data-testcred]').forEach((el) => el.addEventListener('click', () => void testCredential(vc, el.dataset.testcred!)))
  root.querySelectorAll<HTMLElement>('[data-editcred]').forEach((el) =>
    el.addEventListener('click', () => {
      const n = el.dataset.editcred!
      editName = editName === n ? null : n
      vc.rerender()
    }),
  )
  root.querySelector<HTMLElement>('[data-editcancel]')?.addEventListener('click', () => {
    editName = null
    vc.rerender()
  })
  root.querySelectorAll<HTMLElement>('[data-delcred]').forEach((el) =>
    el.addEventListener('click', () => {
      if (confirm(`Delete credential ${el.dataset.delcred}? Agents using it will need reassigning.`)) void deleteCredential(vc, el.dataset.delcred!)
    }),
  )
  // Click a discovered model chip → set that credential's model.
  root.querySelectorAll<HTMLElement>('[data-setmodel]').forEach((el) =>
    el.addEventListener('click', () => {
      const name = el.dataset.setmodel!
      const model = el.dataset.model!
      const c = vc.store.credentials.find((x) => x.name === name)
      void createCredential(vc, { name, provider: 'openai-compatible', baseURL: c?.baseURL, model }).then(() => {
        tested.delete(name)
        vc.rerender()
      })
    }),
  )
  // Rotate / model edit form.
  const rf = root.querySelector<HTMLFormElement>('.agents-rotate-form')
  rf?.addEventListener('submit', (e) => {
    e.preventDefault()
    const name = rf.dataset.rotate!
    const g = (n: string) => (rf.querySelector<HTMLInputElement>(`[name=${n}]`)?.value || '').trim()
    const body: Record<string, unknown> = { name, provider: 'openai-compatible', model: g('model'), baseURL: g('baseURL') }
    const key = g('apiKey')
    if (key) body.apiKey = key
    void createCredential(vc, body).then((ok) => {
      if (ok) {
        editName = null
        tested.delete(name)
        vc.rerender()
      }
    })
  })
  // Create form.
  const cf = root.querySelector<HTMLFormElement>('.agents-model-create')
  if (cf) {
    const preset = cf.querySelector<HTMLSelectElement>('select[name=preset]')!
    const baseURL = cf.querySelector<HTMLInputElement>('input[name=baseURL]')!
    const model = cf.querySelector<HTMLInputElement>('input[name=model]')!
    preset.addEventListener('change', () => {
      const p = PROVIDER_PRESETS.find((x) => x.id === preset.value)
      if (p && p.id !== 'custom') baseURL.value = p.baseURL
      if (p) model.placeholder = p.modelHint
    })
    cf.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (cf.querySelector<HTMLInputElement>(`input[name=${n}]`)?.value || '').trim()
      void createCredential(vc, { name: g('name'), provider: 'openai-compatible', baseURL: baseURL.value.trim(), model: model.value.trim(), apiKey: g('apiKey') }).then((ok) => {
        if (ok) creating = false
      })
    })
  }
}
