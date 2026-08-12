// VibeStudioElement is the custom element the faros portal renders for this
// provider — the wizard + studio UI. The host portal owns all state
// transitions; this element renders views, posts submissions, and polls the
// session view + event log.
//
// Layout identity: the conversation is soft (bubbles, sentence case); the
// machine's work is precise (the "build ledger" — a monospace timeline of
// tool activity with real durations, grouped per work burst). Right pane
// tabs: Preview (live iframe) / Code (read-only workspace browser) / Status
// (checkpoints + project facts).
//
// Auth model: "hub-proxy" (see portalkit/tenant.ts) — bearer + X-Faros-Org/
// -Workspace on every call; the hub resolves them into X-Faros-Tenant.

import { hasWorkspace, serviceBase, tenantHeaders } from './portalkit/tenant'
import { ic } from './portalkit/icons'
import { createEditor, type EditorHandle } from './editor'

export interface FarosContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  // subPath is what the shell's router parsed after /providers/vibe-studio/.
  // We use "sessions/<id>" so a refresh, a bookmark, or browser back/forward
  // lands on the same conversation.
  subPath?: string
}

interface QuestionOption {
  label: string
  recommended?: boolean
}

interface Question {
  id: string
  text: string
  options: QuestionOption[]
}

interface Blueprint {
  title: string
  summary: string
  template: { name: string; reason?: string }
  assumptions?: string[]
  successCriteria?: string[]
  questions?: Question[]
}

interface Checkpoint {
  name: string
  state: string
  reason?: string
}

interface SessionView {
  id: string
  phase: 'intake' | 'review' | 'provisioning' | 'studio'
  nextAction: string
  projectName?: string
  previewURL?: string
  partial?: string
  blueprint?: Blueprint
  questions?: Question[]
  checkpoints?: Record<string, Checkpoint>
  lastOrdinal: number
}

interface PromotionComponent {
  name: string
  imageInput: string
  image?: string
  built?: boolean
}

interface PromotionView {
  components?: PromotionComponent[]
  instance?: string
  phase?: string
  url?: string
  revision?: string
  committed?: boolean
  commitSHA?: string
}

interface SessionEvent {
  ordinal: number
  type: string
  at?: string
  data?: Record<string, unknown>
}

interface SessionRecord {
  id: string
  preview?: string
  phase: string
  createdAt: string
  updatedAt: string
}

interface ModelRecord {
  name: string
  displayName?: string
  provider?: string
  baseURL?: string
  model: string
  default?: boolean
}

interface ProjectRecord {
  name: string
  displayName: string
  template?: string
  phase?: string
  previewURL?: string
  sessionID?: string
  updatedAt?: string
}

interface ActivityData {
  tool?: string
  detail?: string
  ok?: boolean
  error?: string
  durationMS?: number
}

type Tab = 'preview' | 'code' | 'status'
type HomeView = 'projects' | 'models'

// Presets fill the form; every field stays editable, so a model id we don't
// list (or a newer one) is always one keystroke away. OpenAI leads because
// it's the most common starting point.
interface ModelPreset {
  group: string
  label: string
  model: string
  baseURL: string
}

const MODEL_PRESETS: ModelPreset[] = [
  { group: 'OpenAI', label: 'GPT-5.4', model: 'gpt-5.4', baseURL: 'https://api.openai.com/v1' },
  { group: 'OpenAI', label: 'GPT-5', model: 'gpt-5', baseURL: 'https://api.openai.com/v1' },
  { group: 'OpenAI', label: 'GPT-4.1', model: 'gpt-4.1', baseURL: 'https://api.openai.com/v1' },
  { group: 'OpenAI', label: 'o4-mini', model: 'o4-mini', baseURL: 'https://api.openai.com/v1' },
  { group: 'Anthropic', label: 'Claude Fable 5', model: 'claude-fable-5', baseURL: 'https://api.anthropic.com/v1/' },
  { group: 'Anthropic', label: 'Claude Opus 5', model: 'claude-opus-5', baseURL: 'https://api.anthropic.com/v1/' },
  { group: 'Anthropic', label: 'Claude Sonnet 5', model: 'claude-sonnet-5', baseURL: 'https://api.anthropic.com/v1/' },
  { group: 'Anthropic', label: 'Claude Haiku 4.5', model: 'claude-haiku-4-5-20251001', baseURL: 'https://api.anthropic.com/v1/' },
  { group: 'Local', label: 'Ollama (llama3.1)', model: 'llama3.1', baseURL: 'http://localhost:11434/v1' },
]

export class VibeStudioElement extends HTMLElement {
  private _ctx: FarosContext | null = null
  private _view: SessionView | null = null
  private _events: SessionEvent[] = []
  private _sessions: SessionRecord[] = []
  private _projects: ProjectRecord[] = []
  private _sessionsLoaded = false
  private _error = ''
  private _busy = false
  private _pollTimer: number | null = null
  private _tab: Tab = 'preview'
  private _filePaths: string[] = []
  private _promotion: PromotionView | null = null
  private _promotionFor = ''
  private _promotionAt = -1
  private _shipMsg = ''
  private _filesLoadedAt = 0
  private _activeFile: { path: string; content: string } | null = null
  private _sessionModel = ''
  private _editor: EditorHandle | null = null
  private _saveState = ''
  private _renderedSig = ''
  private _appliedRoute = ''
  // When the user last scrolled the conversation, and whether the scroll we
  // are seeing is our own. Renders rebuild the transcript DOM, so touching
  // scroll mid-gesture fights the user; we defer instead.
  private _userScrollAt = 0
  private _programmaticScroll = false
  private _models: ModelRecord[] = []
  private _homeView: HomeView = 'projects'
  private _addingModel = false
  private _modelsUnavailable = ''

  set farosContext(v: FarosContext | null) {
    // The portal sets this property AFTER appending the element; the token
    // arrives here. (Re)load the home list once real credentials land.
    const hadToken = !!this._ctx?.token
    this._ctx = v
    this._render()
    if (v?.token && !hadToken) void this._loadSessions()
    // URL is the source of truth for which view is open: apply it on first
    // context, on refresh, and on browser back/forward (the shell re-pushes
    // the context whenever subPath changes).
    this._applyRoute(v?.subPath || '')
  }
  get farosContext(): FarosContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._render()
    void this._loadSessions() // standalone debug page path; portal reloads via setter
  }

  disconnectedCallback(): void {
    if (this._pollTimer !== null) window.clearTimeout(this._pollTimer)
  }

  // ── data ──────────────────────────────────────────────────────────────

  private _apiBase(): string {
    return serviceBase(this._ctx?.basePath || '/ui/providers/vibe-studio') + '/api'
  }

  private async _call(method: string, path: string, body?: unknown): Promise<Response> {
    return fetch(this._apiBase() + path, {
      method,
      credentials: 'same-origin',
      headers: tenantHeaders({ token: this._ctx?.token, json: !!body }),
      body: body ? JSON.stringify(body) : undefined,
    })
  }

  private async _loadSessions(): Promise<void> {
    if (!hasWorkspace() && this._ctx) return
    try {
      // Projects (the KRM — source of truth for "your apps") and sessions
      // (drafts / conversations) load together; the home page joins them.
      const [pr, sr, mr] = await Promise.all([
        this._call('GET', '/projects'),
        this._call('GET', '/sessions'),
        this._call('GET', '/models'),
      ])
      if (mr.ok) {
        const j = (await mr.json()) as { items: ModelRecord[]; available?: boolean; reason?: string }
        this._models = j.items || []
        this._modelsUnavailable = j.available === false ? j.reason || 'Models are not available yet.' : ''
      }
      if (pr.ok) {
        const j = (await pr.json()) as { items: ProjectRecord[] }
        this._projects = j.items || []
      }
      if (sr.ok) {
        const j = (await sr.json()) as { items: SessionRecord[] }
        this._sessions = j.items || []
      }
      this._sessionsLoaded = true
    } catch {
      /* home list is best-effort */
    }
    this._render()
  }

  private async _refresh(): Promise<void> {
    if (!this._view) return
    try {
      const r = await this._call('GET', `/sessions/${this._view.id}`)
      if (r.ok) this._view = (await r.json()) as SessionView
      const er = await this._call('GET', `/sessions/${this._view.id}/events`)
      if (er.ok) {
        const j = (await er.json()) as { items: SessionEvent[] }
        this._events = j.items || []
      }
      this._error = ''
    } catch (e) {
      this._error = String(e)
    }
    this._render()
    this._schedulePoll()
  }

  private _schedulePoll(): void {
    if (this._pollTimer !== null) window.clearTimeout(this._pollTimer)
    if (!this._view) return
    const active =
      this._view.nextAction.startsWith('run-') || this._view.nextAction === 'await-turn'
    this._pollTimer = window.setTimeout(() => void this._refresh(), active ? 400 : 3000)
  }

  private async _createSession(input: string): Promise<void> {
    this._busy = true
    this._render()
    try {
      const r = await this._call('POST', '/sessions', { input })
      if (!r.ok) throw new Error(`Couldn’t start the session (${r.status}). Try again.`)
      this._view = (await r.json()) as SessionView
      this._events = []
      this._error = ''
      this._navigate(`sessions/${this._view.id}`)
    } catch (e) {
      this._error = String(e)
    }
    this._busy = false
    this._render()
    this._schedulePoll()
  }

  private async _submit(body: Record<string, unknown>): Promise<void> {
    if (!this._view) return
    this._busy = true
    this._render()
    try {
      const r = await this._call('POST', `/sessions/${this._view.id}/submissions`, body)
      if (!r.ok) throw new Error(`That didn’t go through (${r.status}). Try again.`)
      this._view = (await r.json()) as SessionView
      this._error = ''
    } catch (e) {
      this._error = String(e)
    }
    this._busy = false
    this._render()
    this._schedulePoll()
  }

  // _applyRoute opens or closes a session to match the URL. Guarded on the
  // last applied path so re-pushed contexts (theme, token refresh) don't
  // reload the session on every property set.
  private _applyRoute(subPath: string): void {
    const path = subPath.replace(/^\/+|\/+$/g, '')
    if (path === this._appliedRoute) return
    this._appliedRoute = path
    const m = /^sessions\/([^/]+)$/.exec(path)
    if (m) {
      if (this._view?.id !== m[1]) void this._openSession(m[1], false)
      return
    }
    if (this._view) this._goHome(false)
  }

  // _navigate asks the shell to push a URL (it owns history).
  private _navigate(path: string): void {
    this._appliedRoute = path
    this.dispatchEvent(
      new CustomEvent('faros-navigate', { detail: { path }, bubbles: true, composed: true }),
    )
  }

  private async _openSession(id: string, pushURL = true): Promise<void> {
    if (pushURL) this._navigate(`sessions/${id}`)
    this._busy = true
    this._render()
    try {
      const r = await this._call('GET', `/sessions/${id}`)
      if (!r.ok) throw new Error(`Couldn’t open the project (${r.status}).`)
      this._view = (await r.json()) as SessionView
      // The picker must show the session's actual model, not a stale local
      // guess — read it from the Session CR.
      this._sessionModel = ''
      try {
        const mr = await this._call('GET', `/sessions/${id}/model`)
        if (mr.ok) this._sessionModel = ((await mr.json()) as { model?: string }).model || ''
      } catch {
        /* picker falls back to "workspace default" */
      }
      this._tab = 'preview'
      this._activeFile = null
      this._filePaths = []
      this._filesLoadedAt = 0
      this._error = ''
    } catch (e) {
      this._error = String(e)
    }
    this._busy = false
    void this._refresh()
  }

  private _goHome(pushURL = true): void {
    if (pushURL) this._navigate('')
    if (this._pollTimer !== null) window.clearTimeout(this._pollTimer)
    this._pollTimer = null
    this._view = null
    this._events = []
    this._error = ''
    this._render()
    void this._loadSessions()
  }

  // Promotion (the ship panel). Loaded lazily with the Status tab; the
  // reconcilers own the deployment, so this is a report plus one button.
  private async _loadPromotion(force = false): Promise<void> {
    if (!this._view?.projectName) return
    if (!force && this._promotionFor === this._view.id && this._promotionAt >= this._view.lastOrdinal) return
    this._promotionFor = this._view.id
    this._promotionAt = this._view.lastOrdinal
    try {
      const r = await this._call('GET', `/sessions/${this._view.id}/promotion`)
      if (r.ok) {
        this._promotion = (await r.json()) as PromotionView
        this._render()
      } else {
        this._promotionAt = -1 // try again on the next render
      }
    } catch {
      this._promotionAt = -1
    }
  }

  private async _promote(): Promise<void> {
    if (!this._view || !this._promotion) return
    const images: Record<string, string> = {}
    for (const c of this._promotion.components || []) {
      images[c.name] = this.querySelector<HTMLInputElement>(`#ship-image-${cssID(c.name)}`)?.value.trim() || ''
    }
    this._shipMsg = ''
    this._busy = true
    this._render()
    try {
      const r = await this._call('POST', `/sessions/${this._view.id}/promote`, { images })
      const j = (await r.json().catch(() => ({}))) as { error?: string; instance?: string }
      this._shipMsg = r.ok ? `Shipping to ${j.instance || 'production'}…` : j.error || `Promotion failed (${r.status})`
    } catch (e) {
      this._shipMsg = String(e)
    } finally {
      this._busy = false
      await this._loadPromotion(true)
      this._render()
    }
  }

  private async _loadFiles(force = false): Promise<void> {
    if (!this._view) return
    // Refresh the tree when stale (new events mean the model may have
    // written files) or on demand.
    if (!force && this._filesLoadedAt >= this._view.lastOrdinal && this._filePaths.length) return
    try {
      const r = await this._call('GET', `/sessions/${this._view.id}/files`)
      if (r.ok) {
        const j = (await r.json()) as { items: string[] }
        this._filePaths = j.items || []
        this._filesLoadedAt = this._view.lastOrdinal
        this._render()
      }
    } catch {
      /* tree is best-effort */
    }
  }

  private async _openFile(path: string): Promise<void> {
    if (!this._view) return
    if (this._editor?.dirty() && !window.confirm('Discard unsaved changes in the current file?')) return
    try {
      const r = await this._call(
        'GET',
        `/sessions/${this._view.id}/files/content?path=${encodeURIComponent(path)}`,
      )
      if (r.ok) {
        this._activeFile = (await r.json()) as { path: string; content: string }
        this._saveState = ''
        // Reuse the live editor when there is one — swapping the document
        // keeps history/undo scoped per file without rebuilding the view.
        this._editor?.setDoc(this._activeFile.path, this._activeFile.content)
        this._render()
      }
    } catch {
      /* viewer is best-effort */
    }
  }

  // _mountEditor (re)attaches the editor after every render. Moving the DOM
  // node preserves cursor, scroll, and undo history across re-renders; the
  // view is only created once per session.
  private _mountEditor(): void {
    const mount = this.querySelector<HTMLElement>('#editor-mount')
    if (!mount || !this._activeFile) return
    if (!this._editor) {
      this._editor = createEditor({
        path: this._activeFile.path,
        content: this._activeFile.content,
        dark: this._ctx?.theme === 'dark',
        onChange: () => this._onEditorChange(),
        onSave: () => void this._saveFile(),
      })
    } else if (this._editor.path !== this._activeFile.path) {
      this._editor.setDoc(this._activeFile.path, this._activeFile.content)
    }
    if (this._editor.dom.parentElement !== mount) mount.appendChild(this._editor.dom)
  }

  // Dirty-state changes are reflected without a full re-render (which would
  // fight the editor); only the save button's label needs updating.
  private _onEditorChange(): void {
    const btn = this.querySelector<HTMLButtonElement>('#file-save')
    if (btn) {
      btn.disabled = !this._editor?.dirty()
      btn.textContent = this._editor?.dirty() ? 'Save' : 'Saved'
    }
    const state = this.querySelector<HTMLElement>('#file-state')
    if (state && this._editor?.dirty()) state.textContent = 'Unsaved changes'
  }

  private async _saveFile(): Promise<void> {
    if (!this._view || !this._editor || !this._activeFile) return
    const path = this._editor.path
    const content = this._editor.doc()
    this._saveState = 'Saving…'
    this._onEditorChange()
    const state = this.querySelector<HTMLElement>('#file-state')
    if (state) state.textContent = this._saveState
    try {
      const r = await this._call('PUT', `/sessions/${this._view.id}/files/content`, { path, content })
      if (!r.ok) throw new Error(`Save failed (${r.status}).`)
      const j = (await r.json()) as { synced?: boolean; reason?: string }
      this._editor.markSaved()
      this._activeFile = { path, content }
      this._saveState = j.synced ? 'Saved and synced to the sandbox' : `Saved — not synced: ${j.reason || 'unknown'}`
    } catch (e) {
      this._saveState = String(e)
    }
    this._onEditorChange()
    const s2 = this.querySelector<HTMLElement>('#file-state')
    if (s2) s2.textContent = this._saveState
  }

  // ── render ────────────────────────────────────────────────────────────

  private _turnElapsedSec(): number {
    if (this._view?.nextAction !== 'await-turn') return 0
    for (let i = this._events.length - 1; i >= 0; i--) {
      if (this._events[i].type === 'turn.started') {
        const t = Date.parse(this._events[i].at || '')
        return Number.isNaN(t) ? 0 : Math.max(0, Math.floor((Date.now() - t) / 1000))
      }
    }
    return 0
  }

  private _stateSig(): string {
    const v = this._view
    return JSON.stringify([
      this._error,
      this._busy,
      !!this._ctx,
      v ? [v.id, v.phase, v.nextAction, v.lastOrdinal, v.projectName, v.previewURL, v.partial] : null,
      this._events.length ? this._events[this._events.length - 1].ordinal : 0,
      this._sessionsLoaded,
      this._sessions.map((s) => s.id + s.phase + s.updatedAt),
      this._projects.map((p) => p.name + p.phase + p.previewURL),
      this._models.map((m) => m.name + m.default),
      this._modelsUnavailable,
      this._homeView,
      this._addingModel,
      this._sessionModel,
      this._tab,
      this._filePaths.length,
      this._activeFile?.path,
      this._promotion ? [this._promotion.phase, this._promotion.url, this._promotion.revision, this._promotion.committed] : null,
      this._shipMsg,
      this._turnElapsedSec(),
    ])
  }

  private _render(): void {
    const sig = this._stateSig()
    if (sig === this._renderedSig) return

    const active = document.activeElement as HTMLElement | null
    // Never rebuild the DOM under an open dropdown or a control the user is
    // operating — the poll loop would otherwise close/reset it mid-choice.
    // The pending signature is dropped so the next tick re-renders.
    if (active && this.contains(active) && active.tagName === 'SELECT') return
    // Same for the code editor: typing must not be interrupted by a poll.
    if (this._editor?.focused()) return

    this._renderedSig = sig
    const focusId = active && this.contains(active) ? active.id : ''
    const saved: Record<string, string> = {}
    this.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(
      'input[type=text], textarea, select',
    ).forEach((el) => {
      if (el.id) saved[el.id] = el.value
    })
    // Rebuilding innerHTML resets scrollTop to 0, which reads as the view
    // jumping to the top every time the assistant emits an event. Capture
    // the position (and which ledgers are expanded) to restore after.
    const prevScroll = this.querySelector<HTMLElement>('#chat-scroll')
    const stickBottom =
      !prevScroll || prevScroll.scrollHeight - prevScroll.scrollTop - prevScroll.clientHeight < 60
    const savedTop = prevScroll ? prevScroll.scrollTop : 0

    // Reading history while events stream in: leave the DOM alone entirely
    // until the gesture settles. The signature is not consumed, so the next
    // poll renders. (Following at the bottom is unaffected.)
    if (!stickBottom && Date.now() - this._userScrollAt < 1200) return
    const openLedgers = new Set<number>()
    this.querySelectorAll<HTMLDetailsElement>('.ledger').forEach((d, i) => {
      if (d.open) openLedgers.add(i)
    })

    this._renderDOM()

    // Restore what the user was looking at: pinned to the newest message if
    // they were already there, otherwise exactly where they left off.
    const nextScroll = this.querySelector<HTMLElement>('#chat-scroll')
    if (nextScroll) {
      const want = stickBottom ? nextScroll.scrollHeight : savedTop
      // Assign only when it actually differs: a redundant write still fires
      // a scroll event and can nudge an in-flight gesture.
      if (Math.abs(nextScroll.scrollTop - want) > 1) {
        this._programmaticScroll = true
        nextScroll.scrollTop = want
      }
      nextScroll.addEventListener('scroll', this._onChatScroll, { passive: true })
    }
    if (openLedgers.size > 0) {
      this.querySelectorAll<HTMLDetailsElement>('.ledger').forEach((d, i) => {
        if (openLedgers.has(i)) d.open = true
      })
    }
    this._mountEditor()
    for (const [id, value] of Object.entries(saved)) {
      const el = this.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(`#${id}`)
      // Selects render their own selected state from component data, so only
      // restore a text control the user had typed into.
      if (el && el.tagName !== 'SELECT' && !el.value) el.value = value
    }
    if (focusId) {
      const el = this.querySelector<HTMLElement>(`#${focusId}`)
      if (el) {
        el.focus()
        const input = el as HTMLInputElement
        if (typeof input.setSelectionRange === 'function' && typeof input.value === 'string') {
          input.setSelectionRange(input.value.length, input.value.length)
        }
      }
    }
  }

  // _onChatScroll records genuine user scrolling; our own restores set a
  // flag first so they don't count as the user reading.
  private _onChatScroll = (): void => {
    if (this._programmaticScroll) {
      this._programmaticScroll = false
      return
    }
    this._userScrollAt = Date.now()
  }

  private _renderDOM(): void {
    const v = this._view
    this.innerHTML = `
      <div class="vibe-shell ${v?.phase === 'studio' ? 'wide' : ''}">
        <header class="vibe-head">
          ${v ? `<button id="back-home" class="ghost back">← Projects</button>` : ''}
          <h1>Vibe Studio</h1>
          ${v ? `<span class="phase-chip phase-${v.phase}">${v.phase}</span>` : ''}
          ${v ? this._headStatus(v) : ''}
        </header>
        ${this._error ? `<div class="vibe-error">${esc(this._error)}</div>` : ''}
        <main class="vibe-main">${this._renderBody(v)}</main>
      </div>
    `
    this._bind()
  }

  private _headStatus(v: SessionView): string {
    const cps = v.checkpoints || {}
    const sandbox = cps['template']
    const dot =
      sandbox?.state === 'done' ? 'ok' : sandbox?.state === 'error' ? 'bad' : 'wait'
    const parts = [`<span class="head-dot ${dot}"></span><span class="head-label">sandbox</span>`]
    if (v.previewURL) {
      parts.push(
        `<a class="head-link" href="${esc(v.previewURL)}" target="_blank" rel="noreferrer">preview ↗</a>`,
      )
    }
    return `<span class="head-status">${parts.join('')}</span>`
  }

  private _renderBody(v: SessionView | null): string {
    if (!v && this._ctx && !hasWorkspace()) {
      return `<section class="panel"><h2>Select a workspace</h2>
        <p class="muted">Vibe Studio builds apps inside a workspace. Pick an
        organization and workspace in the portal header first.</p></section>`
    }
    if (!v) return this._renderHome()
    switch (v.phase) {
      case 'intake':
        if (v.questions && v.questions.length > 0) return this._renderQuestions(v.questions)
        return `<section class="panel center-panel">
          <div class="spinner"></div>
          <p class="muted">Reading your idea and picking the right template…</p>
        </section>`
      case 'review':
        return this._renderBlueprint(v.blueprint)
      case 'provisioning':
        return `<section class="panel">
          <h2>Setting things up</h2>
          <p class="muted">Creating the project, repository, and live sandbox. This
          usually takes a minute or two.</p>
          ${this._renderCheckpoints(v.checkpoints, true)}
        </section>`
      case 'studio':
        return this._renderStudio(v)
    }
  }

  // ── home ──────────────────────────────────────────────────────────────

  // ── models ────────────────────────────────────────────────────────────

  private async _addModel(form: {
    displayName: string
    provider: string
    baseURL: string
    model: string
    apiKey: string
    makeDefault: boolean
  }): Promise<void> {
    this._busy = true
    this._render()
    try {
      const r = await this._call('POST', '/models', {
        displayName: form.displayName,
        provider: form.provider,
        baseURL: form.baseURL,
        model: form.model,
        apiKey: form.apiKey,
        default: form.makeDefault,
      })
      if (!r.ok) throw new Error(`Couldn’t save the model (${r.status}).`)
      this._addingModel = false
      this._error = ''
    } catch (e) {
      this._error = String(e)
    }
    this._busy = false
    void this._loadSessions()
  }

  private async _deleteModel(name: string): Promise<void> {
    if (!window.confirm(`Remove the model “${name}”? Its API key secret is deleted too.`)) return
    try {
      await this._call('DELETE', `/models/${encodeURIComponent(name)}`)
    } catch (e) {
      this._error = String(e)
    }
    void this._loadSessions()
  }

  private async _setDefaultModel(name: string): Promise<void> {
    try {
      await this._call('POST', `/models/${encodeURIComponent(name)}/default`)
    } catch (e) {
      this._error = String(e)
    }
    void this._loadSessions()
  }

  private async _setSessionModel(name: string): Promise<void> {
    if (!this._view) return
    this._sessionModel = name
    try {
      await this._call('PUT', `/sessions/${this._view.id}/model`, { model: name })
    } catch (e) {
      this._error = String(e)
    }
    this._render()
  }

  // _applyPreset fills the editable fields from a preset. Empty model id
  // ("Custom…") clears them so the user types their own.
  private _applyPreset(model: string): void {
    const p = MODEL_PRESETS.find((x) => x.model === model)
    const set = (sel: string, value: string) => {
      const el = this.querySelector<HTMLInputElement>(sel)
      if (el) el.value = value
    }
    set('#m-model', p ? p.model : '')
    set('#m-base', p ? p.baseURL : '')
    const display = this.querySelector<HTMLInputElement>('#m-display')
    if (display && (!display.value || MODEL_PRESETS.some((x) => x.label === display.value))) {
      display.value = p ? p.label : ''
    }
  }

  private _renderModels(): string {
    const rows = this._models
      .map(
        (m) => `
        <tr>
          <td class="res-name">${esc(m.displayName || m.name)}
            <div class="res-sub"><code>${esc(m.model)}</code></div></td>
          <td class="muted">${esc(m.provider || '')}</td>
          <td class="muted">${esc(m.baseURL || '')}</td>
          <td>${m.default ? '<span class="status-badge ok">Default</span>' : `<button class="ghost act-model-default" data-name="${esc(m.name)}">Make default</button>`}</td>
          <td class="res-actions">
            <button class="ghost danger act-model-del" data-name="${esc(m.name)}">Remove</button>
          </td>
        </tr>`,
      )
      .join('')
    const groups = [...new Set(MODEL_PRESETS.map((p) => p.group))]
    const presetOptions = groups
      .map(
        (g) =>
          `<optgroup label="${esc(g)}">${MODEL_PRESETS.filter((p) => p.group === g)
            .map((p) => `<option value="${esc(p.model)}">${esc(p.label)}</option>`)
            .join('')}</optgroup>`,
      )
      .join('')
    const form = this._addingModel
      ? `<section class="panel model-form">
           <h2>Add a model</h2>
           <p class="muted">Pick a model to fill the form, then edit anything — any
           OpenAI-compatible endpoint works. The key is stored in a Secret in this workspace.</p>
           <label class="field"><span>Model</span>
             <select id="m-preset">
               ${presetOptions}
               <option value="">Custom…</option>
             </select></label>
           <label class="field"><span>Model id</span>
             <input type="text" id="m-model" placeholder="gpt-5.4"></label>
           <label class="field"><span>Base URL</span>
             <input type="text" id="m-base" placeholder="https://api.openai.com/v1"></label>
           <label class="field"><span>Name</span>
             <input type="text" id="m-display" placeholder="optional label, e.g. Fast"></label>
           <label class="field"><span>API key</span>
             <input type="text" id="m-key" placeholder="sk-…"></label>
           <label class="option"><input type="checkbox" id="m-default" checked> Use as default for new projects</label>
           <div class="row end">
             <button id="m-cancel" class="ghost">Cancel</button>
             <button id="m-save" ${this._busy ? 'disabled' : ''}>Save model</button>
           </div>
         </section>`
      : `<div class="row end"><button id="m-add">Add model</button></div>`
    if (this._modelsUnavailable) {
      return `<section class="panel">
        <h2>Models aren’t installed yet</h2>
        <p class="muted">${esc(this._modelsUnavailable)}</p>
      </section>`
    }
    return `
      ${form}
      ${
        this._models.length
          ? `<table class="res-table"><thead><tr>
               <th>Name</th><th>Provider</th><th>Endpoint</th><th>Default</th><th></th>
             </tr></thead><tbody>${rows}</tbody></table>`
          : `<section class="panel"><p class="muted">No models configured yet. Add one to start
             building — without it the assistant can’t change code.</p></section>`
      }
    `
  }

  private _renderHome(): string {
    // Apps = Project CRs (kube truth). Drafts = sessions still in the wizard,
    // or studio conversations whose project no longer exists (marked stale).
    const claimed = new Set(this._projects.map((p) => p.sessionID).filter(Boolean))
    const drafts = this._sessions.filter((s) => !claimed.has(s.id))

    // Resource-table listing, matching the faros portal idiom (name /
    // status badge / facts / age / row actions) used by the other providers.
    const appRows = this._projects
      .map(
        (p) => `
        <tr class="res-row" data-sid="${esc(p.sessionID || '')}">
          <td class="res-name">${esc(p.displayName)}<div class="res-sub"><code>${esc(p.name)}</code></div></td>
          <td><span class="status-badge ${badgeClass(p.phase)}">${esc(p.phase || 'Provisioning')}</span></td>
          <td><code class="muted">${esc(p.template || '')}</code></td>
          <td>${p.previewURL ? `<a href="${esc(p.previewURL)}" target="_blank" rel="noreferrer" class="head-link">open ↗</a>` : '<span class="muted">—</span>'}</td>
          <td class="muted">${p.updatedAt ? esc(fmtWhen(p.updatedAt)) : ''}</td>
          <td class="res-actions">
            ${p.sessionID ? `<button class="ghost act-open" data-sid="${esc(p.sessionID)}">Open</button>` : ''}
            <button class="ghost danger act-del-project" data-name="${esc(p.name)}" data-display="${esc(p.displayName)}">Delete</button>
          </td>
        </tr>`,
      )
      .join('')

    const draftRows = drafts
      .map((s) => {
        const stale = s.phase === 'studio'
        return `
        <tr class="res-row ${stale ? 'stale' : ''}" data-sid="${esc(s.id)}">
          <td class="res-name">${esc(s.preview || s.id)}</td>
          <td><span class="status-badge ${stale ? 'warn' : ''}">${stale ? 'sandbox removed' : esc(s.phase)}</span></td>
          <td class="muted">${esc(fmtWhen(s.updatedAt))}</td>
          <td class="res-actions">
            <button class="ghost act-open" data-sid="${esc(s.id)}">Open</button>
            <button class="ghost danger act-del-session" data-sid="${esc(s.id)}" data-display="${esc(s.preview || s.id)}">Delete</button>
          </td>
        </tr>`
      })
      .join('')

    const nav = `
      <nav class="tabs home-tabs" role="tablist">
        <button class="tab ${this._homeView === 'projects' ? 'active' : ''}" data-home="projects" role="tab">Projects</button>
        <button class="tab ${this._homeView === 'models' ? 'active' : ''}" data-home="models" role="tab">Models</button>
        ${this._models.length === 0 && this._sessionsLoaded ? '<span class="nav-hint">← add a model to enable building</span>' : ''}
      </nav>`

    if (this._homeView === 'models') return nav + this._renderModels()

    return `
      ${nav}
      <section class="panel intake">
        <h2>What do you want to build?</h2>
        <p class="muted">Describe your app in a sentence or two. Vibe Studio picks a
        template, spins up a live sandbox, and builds with you.</p>
        <textarea id="intake-input" rows="3" placeholder="e.g. a booking page for my barbershop with email confirmations"></textarea>
        <div class="row end">
          <button id="intake-go" ${this._busy ? 'disabled' : ''}>Start building</button>
        </div>
      </section>
      ${
        this._projects.length
          ? `<h3 class="section-label">Apps</h3>
             <table class="res-table"><thead><tr>
               <th>Name</th><th>Status</th><th>Template</th><th>Preview</th><th>Updated</th><th></th>
             </tr></thead><tbody>${appRows}</tbody></table>`
          : ''
      }
      ${
        drafts.length
          ? `<h3 class="section-label">Drafts &amp; past conversations</h3>
             <table class="res-table"><thead><tr>
               <th>Conversation</th><th>Status</th><th>Updated</th><th></th>
             </tr></thead><tbody>${draftRows}</tbody></table>`
          : ''
      }
      ${!this._sessionsLoaded ? `<p class="muted">Loading…</p>` : ''}
    `
  }

  private async _deleteSession(id: string, display: string): Promise<void> {
    if (!window.confirm(`Delete “${display}”?\n\nThis removes the conversation, its workspace, and the app it created (sandbox included). The git repository is kept.`)) return
    try {
      const r = await this._call('DELETE', `/sessions/${id}`)
      if (!r.ok && r.status !== 404) throw new Error(`Delete failed (${r.status}).`)
    } catch (e) {
      this._error = String(e)
    }
    void this._loadSessions()
  }

  private async _deleteProject(name: string, display: string): Promise<void> {
    if (!window.confirm(`Delete the app “${display}”?\n\nThis tears down its sandbox and instances. The conversation and git repository are kept.`)) return
    try {
      const r = await this._call('DELETE', `/projects/${encodeURIComponent(name)}`)
      if (!r.ok && r.status !== 404) throw new Error(`Delete failed (${r.status}).`)
    } catch (e) {
      this._error = String(e)
    }
    void this._loadSessions()
  }

  // ── wizard ────────────────────────────────────────────────────────────

  private _renderQuestions(questions: Question[]): string {
    const qs = questions
      .map(
        (q) => `
        <fieldset class="question" data-qid="${esc(q.id)}">
          <legend>${esc(q.text)}</legend>
          ${q.options
            .map(
              (o, i) => `
            <label class="option">
              <input type="radio" name="q-${esc(q.id)}" value="${esc(o.label)}" ${o.recommended || (i === 0 && !q.options.some((x) => x.recommended)) ? 'checked' : ''}>
              ${esc(o.label)}${o.recommended ? ' <span class="rec">★ Recommended</span>' : ''}
            </label>`,
            )
            .join('')}
          <label class="option custom">
            <input type="radio" name="q-${esc(q.id)}" value="__custom__">
            ✎ Custom answer… <input type="text" class="custom-text" data-qid="${esc(q.id)}">
          </label>
        </fieldset>`,
      )
      .join('')
    return `
      <section class="panel">
        <h2>A couple of decisions</h2>
        ${qs}
        <div class="row end"><button id="answers-go" ${this._busy ? 'disabled' : ''}>Continue</button></div>
      </section>
    `
  }

  private _renderBlueprint(bp?: Blueprint): string {
    if (!bp) return `<section class="panel center-panel"><div class="spinner"></div></section>`
    return `
      <section class="panel blueprint">
        <h2>${esc(bp.title || 'Your app')}</h2>
        <p>${esc(bp.summary || '')}</p>
        <dl>
          <dt>Template</dt>
          <dd><code>${esc(bp.template.name)}</code>${bp.template.reason ? ` — ${esc(bp.template.reason)}` : ''}</dd>
          ${bp.assumptions?.length ? `<dt>Assumptions</dt><dd>${bp.assumptions.map(esc).join('<br>')}</dd>` : ''}
          ${bp.successCriteria?.length ? `<dt>Done when</dt><dd>${bp.successCriteria.map(esc).join('<br>')}</dd>` : ''}
        </dl>
        <div class="row">
          <button id="approve-go" ${this._busy ? 'disabled' : ''}>Create app</button>
          <input type="text" id="adjust-text" placeholder="…or tell me what to change">
          <button id="adjust-go" class="ghost" ${this._busy ? 'disabled' : ''}>Adjust</button>
        </div>
      </section>
    `
  }

  private _renderCheckpoints(cps?: Record<string, Checkpoint>, live = false): string {
    const order = ['template', 'git', 'ci', 'production']
    const labels: Record<string, string> = {
      template: 'Sandbox',
      git: 'Repository',
      ci: 'Builds',
      production: 'Production',
    }
    const rows = order
      .map((name) => {
        const cp = cps?.[name]
        const state = cp?.state || 'pending'
        const icon =
          state === 'done'
            ? `<span class="cp-icon ok">${ic('check')}</span>`
            : state === 'error'
              ? `<span class="cp-icon bad">${ic('x')}</span>`
              : state === 'blocked'
                ? `<span class="cp-icon warn">${ic('alert-triangle')}</span>`
                : live
                  ? '<span class="spinner tiny"></span>'
                  : '<span class="cp-icon idle">·</span>'
        return `<li class="cp cp-${state}">${icon}
          <span class="cp-name">${labels[name]}</span>
          ${cp?.reason ? `<span class="muted cp-reason">${esc(cp.reason)}</span>` : ''}</li>`
      })
      .join('')
    return `<ul class="checkpoints">${rows}</ul>`
  }

  // ── studio ────────────────────────────────────────────────────────────

  private _renderStudio(v: SessionView): string {
    return `
      <div class="studio-grid">
        <section class="chat-pane panel">
          <div class="chat-scroll" id="chat-scroll">
            ${this._renderTranscript(v)}
          </div>
          ${this._renderThinking(v)}
          <div class="chat-input">
            <textarea id="chat-text" rows="1" placeholder="What should we build next?" ${v.nextAction === 'await-turn' ? 'disabled' : ''}></textarea>
            <button id="chat-go" ${this._busy || v.nextAction === 'await-turn' ? 'disabled' : ''} aria-label="Send">↑</button>
          </div>
          <div class="chat-foot">
            <label class="model-picker">
              <span class="muted">Model</span>
              <select id="session-model">
                <option value="" ${this._sessionModel ? '' : 'selected'}>Workspace default${defaultModelLabel(this._models)}</option>
                ${this._models
                  .map(
                    (m) =>
                      `<option value="${esc(m.name)}" ${this._sessionModel === m.name ? 'selected' : ''}>${esc(m.displayName || m.name)}</option>`,
                  )
                  .join('')}
              </select>
            </label>
            ${this._models.length === 0 ? '<span class="muted">No models configured — the assistant can’t edit code yet.</span>' : ''}
          </div>
        </section>
        <section class="side-pane panel">
          <nav class="tabs" role="tablist">
            ${(['preview', 'code', 'status'] as Tab[])
              .map(
                (t) =>
                  `<button class="tab ${this._tab === t ? 'active' : ''}" data-tab="${t}" role="tab">${t[0].toUpperCase() + t.slice(1)}</button>`,
              )
              .join('')}
            ${this._tab === 'preview' && v.previewURL ? `<span class="tab-actions"><button id="preview-reload" class="ghost" title="Reload preview">⟳</button><a class="ghost btnlike" href="${esc(v.previewURL)}" target="_blank" rel="noreferrer" title="Open in a new tab">↗</a></span>` : ''}
            ${this._tab === 'code' ? `<span class="tab-actions"><button id="files-reload" class="ghost" title="Refresh files">⟳</button></span>` : ''}
          </nav>
          <div class="tab-body">${this._renderTab(v)}</div>
        </section>
      </div>
    `
  }

  private _renderTranscript(v: SessionView): string {
    type Block = { kind: 'msg'; who: string; text: string; at?: string } | { kind: 'work'; items: ActivityData[] }
    const blocks: Block[] = []
    for (const e of this._events) {
      if (e.type === 'message.user' || e.type === 'message.assistant') {
        blocks.push({
          kind: 'msg',
          who: e.type === 'message.user' ? 'user' : 'assistant',
          text: ((e.data as { text?: string }) || {}).text || '',
          at: e.at,
        })
      } else if (e.type === 'turn.activity') {
        const last = blocks[blocks.length - 1]
        if (last && last.kind === 'work') last.items.push((e.data || {}) as ActivityData)
        else blocks.push({ kind: 'work', items: [(e.data || {}) as ActivityData] })
      }
    }
    const html = blocks
      .map((b, i) => {
        if (b.kind === 'msg') {
          return `<div class="bubble-row ${b.who}">
            <div class="bubble">${md(b.text)}</div>
            ${b.at ? `<time class="stamp">${fmtTime(b.at)}</time>` : ''}
          </div>`
        }
        const total = b.items.reduce((sum, a) => sum + (a.durationMS || 0), 0)
        const failed = b.items.some((a) => a.ok === false)
        const isLast = i === blocks.length - 1
        const open = isLast && v.nextAction === 'await-turn'
        const rows = b.items
          .map(
            (a) => `<li class="ledger-row ${a.ok === false ? 'failed' : ''}" ${a.error ? `title="${esc(a.error)}"` : ''}>
              <span class="ledger-mark">${a.ok === false ? ic('x') : ic('check')}</span>
              <span class="ledger-tool">${esc(a.tool || '')}</span>
              <span class="ledger-detail">${esc(a.detail || '')}</span>
              <span class="ledger-dur">${fmtDur(a.durationMS)}</span>
            </li>${
              a.error
                ? `<li class="ledger-error">${esc(a.error.length > 220 ? a.error.slice(0, 220) + '…' : a.error)}</li>`
                : ''
            }`,
          )
          .join('')
        return `<details class="ledger" ${open ? 'open' : ''}>
          <summary>${failed ? ic('alert-triangle') : ic('settings')} Worked — ${b.items.length} step${b.items.length === 1 ? '' : 's'}${total ? ' · ' + fmtDur(total) : ''}</summary>
          <ul>${rows}</ul>
        </details>`
      })
      .join('')
    return (
      html ||
      `<div class="chat-empty">
        <p><strong>Your app is live in the sandbox.</strong></p>
        <p class="muted">Ask for anything — the assistant reads the code, edits it, and
        the preview updates in place. Try “make the header sticky” or “add a contact form”.</p>
      </div>`
    )
  }

  private _renderThinking(v: SessionView): string {
    if (v.nextAction !== 'await-turn') return ''
    const secs = this._turnElapsedSec()
    return `<div class="thinking">
      <span class="spinner tiny"></span>
      <span class="thinking-partial">${v.partial ? md(lastLines(v.partial, 2)) : 'Working…'}</span>
      <span class="thinking-time">${secs}s</span>
    </div>`
  }

  // The ship panel. Production is a second environment on the same Project,
  // so promoting is one write and the Project reconciler does the rest.
  private _renderShip(v: SessionView): string {
    if (!v.projectName) return ''
    const p = this._promotion
    if (!p) return ''
    const shipped = !!p.instance
    const components = p.components || []
    // Ready to ship when every component has an image to ship: built from
    // this commit, already running, or typed in by hand.
    const unresolved = components.filter((c) => !c.image).map((c) => c.name)
    const blocked = !p.committed || unresolved.length > 0
    const short = (p.commitSHA || '').slice(0, 7)
    const fields = (p.components || [])
      .map(
        (c) => `<label class="ship-field">
          <span>${esc(c.name)}</span>
          <input type="text" id="ship-image-${esc(cssID(c.name))}" value="${esc(c.image || '')}"
                 placeholder="ghcr.io/org/${esc(c.name)}@sha256:…">
          <em class="ship-origin ${c.built ? 'ok' : ''}">${
            c.built
              ? `built from ${esc(short)}`
              : p.committed
                ? `no build for ${esc(short)} yet`
                : 'waiting for the commit'
          }</em>
        </label>`,
      )
      .join('')
    return `<section class="ship">
      <h3>Ship to production</h3>
      <p class="muted">Production runs the images your repository's CI built from the
      commit you are on, alongside the development sandbox. Override one only if you
      want to ship something else.</p>
      ${fields || '<p class="muted">This template has nothing to build.</p>'}
      ${
        shipped
          ? `<dl class="facts">
              <dt>Deployment</dt><dd><code>${esc(p.instance || '')}</code>${p.phase ? ` — ${esc(p.phase)}` : ''}</dd>
              ${p.revision ? `<dt>Revision</dt><dd><code>${esc(p.revision.slice(0, 7))}</code></dd>` : ''}
              ${p.url ? `<dt>Address</dt><dd><a href="${esc(p.url)}" target="_blank" rel="noreferrer">${esc(p.url)}</a></dd>` : ''}
            </dl>`
          : ''
      }
      <div class="row">
        <button id="ship-go" ${this._busy || blocked || !components.length ? 'disabled' : ''}>
          ${shipped ? 'Update production' : 'Ship it'}
        </button>
        ${
          !p.committed
            ? '<span class="muted">Waiting for your latest changes to reach git — promotion always ships a commit.</span>'
            : unresolved.length
              ? `<span class="muted">Waiting for CI to publish ${unresolved.map(esc).join(', ')} for <code>${esc(short)}</code>.</span>`
              : `<span class="muted">Ships <code>${esc(short)}</code></span>`
        }
      </div>
      ${this._shipMsg ? `<p class="ship-msg">${esc(this._shipMsg)}</p>` : ''}
    </section>`
  }

  private _renderTab(v: SessionView): string {
    switch (this._tab) {
      case 'preview':
        if (!v.previewURL) {
          return `<div class="tab-placeholder">
            <div class="spinner"></div>
            <p class="muted">The live preview appears here once the sandbox publishes
            its address — usually within a minute of setup.</p>
          </div>`
        }
        return `<iframe class="preview" src="${esc(v.previewURL)}" title="app preview"></iframe>`
      case 'code': {
        void this._loadFiles()
        const tree = this._filePaths
          .map(
            (p) =>
              `<button class="file-row ${this._activeFile?.path === p ? 'active' : ''}" data-path="${esc(p)}">${esc(p)}</button>`,
          )
          .join('')
        return `<div class="code-split">
          <div class="file-tree">${tree || '<p class="muted pad">No files yet.</p>'}</div>
          <div class="file-view">${
            this._activeFile
              ? `<div class="file-head">
                   <code>${esc(this._activeFile.path)}</code>
                   <span id="file-state" class="muted file-state">${esc(this._saveState)}</span>
                   <button id="file-save" class="ghost" disabled>Saved</button>
                 </div>
                 <div id="editor-mount" class="editor-mount"></div>`
              : '<p class="muted pad">Select a file to open it. Edits here save to the workspace and sync into the running sandbox — the same path the assistant uses. ⌘S / Ctrl-S saves.</p>'
          }</div>
        </div>`
      }
      case 'status':
        void this._loadPromotion()
        return `<div class="pad">
          ${this._renderCheckpoints(v.checkpoints)}
          ${this._renderShip(v)}
          <dl class="facts">
            ${v.projectName ? `<dt>Project</dt><dd><code>${esc(v.projectName)}</code></dd>` : ''}
            ${v.blueprint ? `<dt>Template</dt><dd><code>${esc(v.blueprint.template.name)}</code></dd>` : ''}
            ${v.blueprint?.summary ? `<dt>Summary</dt><dd>${esc(v.blueprint.summary)}</dd>` : ''}
            ${v.previewURL ? `<dt>Preview</dt><dd><a href="${esc(v.previewURL)}" target="_blank" rel="noreferrer">${esc(v.previewURL)}</a></dd>` : ''}
            <dt>Session</dt><dd><code>${esc(v.id)}</code></dd>
          </dl>
        </div>`
    }
  }

  // ── events ────────────────────────────────────────────────────────────

  private _bind(): void {
    this.querySelector<HTMLButtonElement>('#back-home')?.addEventListener('click', () => this._goHome())
    this.querySelectorAll<HTMLButtonElement>('[data-home]').forEach((btn) => {
      btn.addEventListener('click', () => {
        this._homeView = (btn.dataset.home as HomeView) || 'projects'
        this._render()
      })
    })
    this.querySelector<HTMLButtonElement>('#m-add')?.addEventListener('click', () => {
      this._addingModel = true
      this._render()
      // Seed the form from the first preset so "add + paste key + save" works.
      this._applyPreset(MODEL_PRESETS[0].model)
    })
    this.querySelector<HTMLSelectElement>('#m-preset')?.addEventListener('change', (e) => {
      this._applyPreset((e.target as HTMLSelectElement).value)
    })
    this.querySelector<HTMLButtonElement>('#m-cancel')?.addEventListener('click', () => {
      this._addingModel = false
      this._render()
    })
    this.querySelector<HTMLButtonElement>('#m-save')?.addEventListener('click', () => {
      const val = (id: string) => this.querySelector<HTMLInputElement>(id)?.value.trim() || ''
      const model = val('#m-model')
      const apiKey = val('#m-key')
      if (!model || !apiKey) {
        this._error = 'Model id and API key are required.'
        this._render()
        return
      }
      void this._addModel({
        displayName: val('#m-display'),
        provider: 'openai-compatible',
        baseURL: val('#m-base'),
        model,
        apiKey,
        makeDefault: this.querySelector<HTMLInputElement>('#m-default')?.checked ?? true,
      })
    })
    this.querySelectorAll<HTMLButtonElement>('.act-model-del').forEach((btn) => {
      btn.addEventListener('click', () => void this._deleteModel(btn.dataset.name || ''))
    })
    this.querySelectorAll<HTMLButtonElement>('.act-model-default').forEach((btn) => {
      btn.addEventListener('click', () => void this._setDefaultModel(btn.dataset.name || ''))
    })
    this.querySelector<HTMLSelectElement>('#session-model')?.addEventListener('change', (e) => {
      void this._setSessionModel((e.target as HTMLSelectElement).value)
    })
    this.querySelectorAll<HTMLButtonElement>('.act-open').forEach((btn) => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation()
        const sid = btn.dataset.sid
        if (sid) void this._openSession(sid)
      })
    })
    this.querySelectorAll<HTMLElement>('.res-row').forEach((row) => {
      row.addEventListener('click', () => {
        const sid = row.dataset.sid
        if (sid) void this._openSession(sid)
      })
    })
    this.querySelectorAll<HTMLButtonElement>('.act-del-session').forEach((btn) => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation()
        void this._deleteSession(btn.dataset.sid || '', btn.dataset.display || '')
      })
    })
    this.querySelectorAll<HTMLButtonElement>('.act-del-project').forEach((btn) => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation()
        void this._deleteProject(btn.dataset.name || '', btn.dataset.display || '')
      })
    })
    this.querySelector<HTMLButtonElement>('#intake-go')?.addEventListener('click', () => {
      const input = this.querySelector<HTMLTextAreaElement>('#intake-input')?.value.trim()
      if (input) void this._createSession(input)
    })
    this.querySelector<HTMLButtonElement>('#answers-go')?.addEventListener('click', () => {
      const answers: Record<string, string> = {}
      this.querySelectorAll<HTMLFieldSetElement>('fieldset.question').forEach((fs) => {
        const qid = fs.dataset.qid || ''
        const chosen = fs.querySelector<HTMLInputElement>('input[type=radio]:checked')
        if (!chosen) return
        answers[qid] =
          chosen.value === '__custom__'
            ? fs.querySelector<HTMLInputElement>('.custom-text')?.value.trim() || ''
            : chosen.value
      })
      void this._submit({ kind: 'answers', answers })
    })
    this.querySelector<HTMLButtonElement>('#ship-go')?.addEventListener('click', () => {
      void this._promote()
    })
    this.querySelector<HTMLButtonElement>('#approve-go')?.addEventListener('click', () => {
      void this._submit({ kind: 'approve' })
    })
    this.querySelector<HTMLButtonElement>('#adjust-go')?.addEventListener('click', () => {
      const el = this.querySelector<HTMLInputElement>('#adjust-text')
      const text = el?.value.trim()
      if (!text) return
      if (el) el.value = ''
      void this._submit({ kind: 'input', text })
    })
    const send = () => {
      const el = this.querySelector<HTMLTextAreaElement>('#chat-text')
      const text = el?.value.trim()
      if (!text) return
      if (el) el.value = ''
      void this._submit({ kind: 'input', text })
    }
    this.querySelector<HTMLButtonElement>('#chat-go')?.addEventListener('click', send)
    const chatText = this.querySelector<HTMLTextAreaElement>('#chat-text')
    chatText?.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault()
        send()
      }
    })
    chatText?.addEventListener('input', () => {
      chatText.style.height = 'auto'
      chatText.style.height = Math.min(chatText.scrollHeight, 140) + 'px'
    })
    this.querySelectorAll<HTMLButtonElement>('.tab').forEach((tab) => {
      tab.addEventListener('click', () => {
        this._tab = (tab.dataset.tab as Tab) || 'preview'
        if (this._tab === 'code') void this._loadFiles()
        this._render()
      })
    })
    this.querySelector<HTMLButtonElement>('#preview-reload')?.addEventListener('click', () => {
      const frame = this.querySelector<HTMLIFrameElement>('iframe.preview')
      if (frame) frame.src = frame.src
    })
    this.querySelector<HTMLButtonElement>('#files-reload')?.addEventListener('click', () => {
      void this._loadFiles(true)
    })
    this.querySelectorAll<HTMLButtonElement>('.file-row').forEach((row) => {
      row.addEventListener('click', () => {
        const p = row.dataset.path
        if (p) void this._openFile(p)
      })
    })
    this.querySelector<HTMLButtonElement>('#file-save')?.addEventListener('click', () => {
      void this._saveFile()
    })
  }
}

// ── helpers ─────────────────────────────────────────────────────────────

function defaultModelLabel(models: ModelRecord[]): string {
  const d = models.find((m) => m.default)
  return d ? ` (${d.displayName || d.model})` : ''
}

function badgeClass(phase?: string): string {
  switch ((phase || '').toLowerCase()) {
    case 'ready':
      return 'ok'
    case 'provisioning':
    case '':
      return 'warn'
    default:
      return ''
  }
}

function fmtWhen(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const mins = Math.round((Date.now() - t) / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  if (mins < 60 * 24) return `${Math.round(mins / 60)}h ago`
  return new Date(t).toLocaleDateString()
}

function fmtTime(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function fmtDur(ms?: number): string {
  if (!ms) return ''
  return ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : ms + 'ms'
}

function lastLines(s: string, n: number): string {
  const lines = s.trimEnd().split('\n')
  return lines.slice(-n).join('\n')
}

// md renders a safe, minimal markdown subset: escaped HTML, fenced code,
// inline code, bold, newlines.
function md(s: string): string {
  let out = esc(s)
  out = out.replace(/```([a-z]*)\n([\s\S]*?)```/g, (_m, _lang, code: string) => `<pre class="code">${code}</pre>`)
  out = out.replace(/`([^`\n]+)`/g, '<code>$1</code>')
  out = out.replace(/^⚙ (.+)…$/gm, '<span class="tool-chip">⚙ $1</span>')
  out = out.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
  return out.replace(/\n/g, '<br>')
}

// cssID makes a component name safe for an element id / querySelector.
function cssID(s: string): string {
  return s.replace(/[^a-zA-Z0-9_-]/g, '-')
}

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
