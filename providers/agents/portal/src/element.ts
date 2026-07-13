// AgentsElement is the custom element the kedge portal renders for the agents
// provider. The portal loads main.js (registering this element), appends the
// element, sets element.kedgeContext as a JS property, and listens for bubbled
// events. The element runs in light DOM so the portal's CSS variables cascade.

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

interface Agent {
  metadata: { name: string }
  spec: { displayName?: string; description?: string; models?: Record<string, string>; defaultNotifyConnection?: string }
}
interface Credential {
  name: string
  provider?: string
  baseURL?: string
  model?: string
  hasAPIKey?: boolean
}
interface Schedule {
  metadata: { name: string }
  spec: { agentRef: string; type: string; schedule?: string; timeZone?: string; task?: string; checklist?: string; suspend?: boolean }
  status?: { nextRun?: string; lastRun?: string; disabledReason?: string }
}
interface Connection {
  metadata: { name: string }
  spec: { type: string; displayName?: string; baseURL?: string; channel?: string; auth?: string }
  status?: { phase?: string; webhookPath?: string; oauthConnected?: boolean }
}
interface Trigger {
  metadata: { name: string }
  spec: { agentRef: string; source: string; connectionRef?: string; task?: string; suspend?: boolean }
  status?: { lastFired?: string; webhookPath?: string }
}
interface InboxItem {
  id: string
  agentName: string
  kind: string
  state: string
  prompt: string
  createdAt: string
}
interface ChatMessage {
  role: 'user' | 'assistant' | 'tool'
  content: string
  error?: boolean
}

type Tab = 'agents' | 'schedules' | 'triggers' | 'connections' | 'inbox' | 'models'

// Provider presets fill the base URL; all currently map to the
// OpenAI-compatible backend path (the only one the provider builds today).
const PROVIDER_PRESETS: { id: string; label: string; baseURL: string; modelHint: string }[] = [
  { id: 'openai', label: 'OpenAI', baseURL: 'https://api.openai.com/v1', modelHint: 'gpt-4o' },
  { id: 'anthropic', label: 'Anthropic (Claude, OpenAI-compat)', baseURL: 'https://api.anthropic.com/v1', modelHint: 'claude-sonnet-4-20250514' },
  { id: 'openrouter', label: 'OpenRouter', baseURL: 'https://openrouter.ai/api/v1', modelHint: 'anthropic/claude-sonnet-4' },
  { id: 'custom', label: 'Custom (OpenAI-compatible)', baseURL: '', modelHint: 'model-name' },
]

const CONNECTION_TYPES = ['github', 'mcp', 'websearch', 'http', 'telegram', 'slack', 'smtp']

export class AgentsElement extends HTMLElement {
  private _ctx: KedgeContext | null = null
  private _tab: Tab = 'agents'
  private _agents: Agent[] = []
  private _schedules: Schedule[] = []
  private _connections: Connection[] = []
  private _triggers: Trigger[] = []
  private _inbox: InboxItem[] = []
  private _selected: string | null = null
  private _messages: ChatMessage[] = []
  private _streaming = false
  private _error: string | null = null
  private _note: string | null = null
  private _loadedTenant: string | null = null
  private _credentials: Credential[] = []
  private _modelsMsg: string | null = null

  set kedgeContext(v: KedgeContext | null) {
    this._ctx = v
    this._render()
    this._maybeLoad()
  }
  get kedgeContext(): KedgeContext | null {
    return this._ctx
  }

  connectedCallback(): void {
    this._render()
    this._maybeLoad()
  }

  private _maybeLoad(): void {
    if (!this._ctx?.basePath || !this._hasWorkspace()) return
    const key = this._ctx.tenant || JSON.stringify(this._tenant())
    if (key === this._loadedTenant) return
    this._loadedTenant = key
    this._selected = null
    this._messages = []
    void this._loadAgents()
    void this._loadCredentials()
    void this._loadSchedules()
    void this._loadConnections()
    void this._loadTriggers()
    void this._loadInbox()
  }
  private async _loadTriggers(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      const list = await this._get<{ items?: Trigger[] }>('/api/triggers')
      this._triggers = list.items || []
      this._render()
    } catch {
      /* non-fatal */
    }
  }
  private async _loadInbox(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      const list = await this._get<{ items?: InboxItem[] }>('/api/inbox')
      this._inbox = list.items || []
      this._render()
    } catch {
      /* non-fatal */
    }
  }

  // ---- tenant + request plumbing -------------------------------------------

  private _api(path: string): string {
    const base = (this._ctx?.basePath || '/ui/providers/agents').replace(/^\/ui\/providers\//, '/services/providers/')
    return base + path
  }
  private _tenant(): { orgUUID: string | null; workspaceUUID: string | null } {
    try {
      const raw = localStorage.getItem('kedge:portal:tenant')
      if (!raw) return { orgUUID: null, workspaceUUID: null }
      const p = JSON.parse(raw) as { orgUUID?: string | null; workspaceUUID?: string | null }
      return { orgUUID: p.orgUUID ?? null, workspaceUUID: p.workspaceUUID ?? null }
    } catch {
      return { orgUUID: null, workspaceUUID: null }
    }
  }
  private _hasWorkspace(): boolean {
    const t = this._tenant()
    return !!t.orgUUID && !!t.workspaceUUID
  }
  private _headers(hasBody: boolean): Record<string, string> {
    const t = this._tenant()
    const h: Record<string, string> = { Accept: 'application/json' }
    if (hasBody) h['Content-Type'] = 'application/json'
    if (this._ctx?.token) h.Authorization = `Bearer ${this._ctx.token}`
    if (t.orgUUID) h['X-Kedge-Org'] = t.orgUUID
    if (t.workspaceUUID) h['X-Kedge-Workspace'] = t.workspaceUUID
    return h
  }
  private async _get<T>(path: string): Promise<T> {
    const r = await fetch(this._api(path), { credentials: 'same-origin', headers: this._headers(false) })
    if (!r.ok) throw new Error(`${r.status} ${(await r.json().catch(() => ({})))?.message || r.statusText}`)
    return r.json()
  }
  private async _send<T>(method: string, path: string, body?: unknown): Promise<T> {
    const r = await fetch(this._api(path), {
      method,
      credentials: 'same-origin',
      headers: this._headers(body !== undefined),
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
    if (!r.ok) throw new Error(`${r.status} ${(await r.json().catch(() => ({})))?.message || r.statusText}`)
    return (r.status === 204 ? (undefined as unknown) : r.json()) as Promise<T>
  }

  // ---- data loaders --------------------------------------------------------

  private async _loadAgents(): Promise<void> {
    if (!this._hasWorkspace()) return this._render()
    try {
      const list = await this._get<{ items?: Agent[] }>('/api/agents')
      this._agents = list.items || []
      this._error = null
    } catch (e) {
      this._error = 'Failed to load agents: ' + (e as Error).message
    }
    this._render()
  }
  private async _loadSchedules(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      const list = await this._get<{ items?: Schedule[] }>('/api/schedules')
      this._schedules = list.items || []
    } catch {
      /* tab shows its own empty/error state */
    }
    this._render()
  }
  private async _loadConnections(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      const list = await this._get<{ items?: Connection[] }>('/api/connections')
      this._connections = list.items || []
    } catch {
      /* non-fatal */
    }
    this._render()
  }
  private async _loadCredentials(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      const list = await this._get<{ items?: Credential[] }>('/api/credentials')
      this._credentials = list.items || []
      this._render()
    } catch {
      /* models tab can still create the first credential */
    }
  }

  // ---- actions -------------------------------------------------------------

  private async _createAgent(name: string, modelCredential: string, budgetUSD?: string): Promise<void> {
    try {
      const body: Record<string, unknown> = { name, displayName: name, modelCredential }
      if (budgetUSD) body.budgetUSD = budgetUSD
      await this._send('POST', '/api/agents', body)
      await this._loadAgents()
    } catch (e) {
      this._error = 'Create failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _reassignAgent(name: string, modelCredential: string): Promise<void> {
    try {
      await this._send('PUT', `/api/agents/${encodeURIComponent(name)}`, { modelCredential })
      await this._loadAgents()
    } catch (e) {
      this._error = 'Reassign failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _setNotify(name: string, notifyConnection: string): Promise<void> {
    try {
      await this._send('PUT', `/api/agents/${encodeURIComponent(name)}`, { notifyConnection })
      await this._loadAgents()
    } catch (e) {
      this._error = 'Notify update failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _createCredential(body: Record<string, unknown>): Promise<void> {
    this._modelsMsg = 'Saving…'
    this._render()
    try {
      await this._send('POST', '/api/credentials', body)
      this._modelsMsg = 'Credential saved.'
      await this._loadCredentials()
    } catch (e) {
      this._modelsMsg = 'Save failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _deleteCredential(name: string): Promise<void> {
    try {
      await this._send('DELETE', `/api/credentials/${encodeURIComponent(name)}`)
      await this._loadCredentials()
    } catch (e) {
      this._modelsMsg = 'Delete failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _deleteAgent(name: string): Promise<void> {
    try {
      await this._send('DELETE', `/api/agents/${encodeURIComponent(name)}`)
      if (this._selected === name) this._selected = null
      await this._loadAgents()
    } catch (e) {
      this._error = 'Delete failed: ' + (e as Error).message
      this._render()
    }
  }
  private _select(name: string): void {
    this._selected = name
    this._messages = []
    this._error = null
    this._render()
  }

  private async _createSchedule(body: Record<string, unknown>): Promise<void> {
    try {
      await this._send('POST', '/api/schedules', body)
      this._note = 'Schedule created.'
      await this._loadSchedules()
    } catch (e) {
      this._note = 'Create failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _deleteSchedule(name: string): Promise<void> {
    try {
      await this._send('DELETE', `/api/schedules/${encodeURIComponent(name)}`)
      await this._loadSchedules()
    } catch (e) {
      this._note = 'Delete failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _runSchedule(name: string): Promise<void> {
    this._note = `Running ${name}…`
    this._render()
    try {
      const res = await this._send<{ content: string }>('POST', `/api/schedules/${encodeURIComponent(name)}/run`)
      this._note = `${name} ran: ${res.content?.slice(0, 200) || '(no output)'}`
    } catch (e) {
      this._note = `Run failed: ${(e as Error).message}`
    }
    this._render()
  }

  private async _createConnection(body: Record<string, unknown>): Promise<void> {
    try {
      await this._send('POST', '/api/connections', body)
      this._note = 'Connection created.'
      await this._loadConnections()
    } catch (e) {
      this._note = 'Create failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _deleteConnection(name: string): Promise<void> {
    try {
      await this._send('DELETE', `/api/connections/${encodeURIComponent(name)}`)
      await this._loadConnections()
    } catch (e) {
      this._note = 'Delete failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _testConnection(name: string): Promise<void> {
    this._note = `Testing ${name}…`
    this._render()
    try {
      await this._send('POST', `/api/connections/${encodeURIComponent(name)}/test`)
      this._note = `Test message sent via ${name}. Check the channel.`
    } catch (e) {
      this._note = `Test failed: ${(e as Error).message}`
    }
    this._render()
  }
  private async _oauthConnect(name: string): Promise<void> {
    try {
      const res = await this._send<{ authorizeURL: string }>(
        'POST',
        `/api/connections/${encodeURIComponent(name)}/oauth/authorize`,
        { publicBaseURL: location.origin },
      )
      window.open(res.authorizeURL, '_blank', 'noopener')
      this._note = 'Authorize in the opened tab, then refresh Connections.'
      this._render()
    } catch (e) {
      this._note = `OAuth connect failed: ${(e as Error).message}`
      this._render()
    }
  }
  private async _enableInbound(name: string): Promise<void> {
    this._note = `Enabling inbound for ${name}…`
    this._render()
    try {
      const res = await this._send<{ webhookURL: string; registered: boolean; note: string }>(
        'POST',
        `/api/connections/${encodeURIComponent(name)}/enable-inbound`,
        { publicBaseURL: location.origin },
      )
      this._note = `${res.registered ? '✅' : 'ℹ️'} ${res.note} URL: ${res.webhookURL}`
      await this._loadConnections()
    } catch (e) {
      this._note = `Enable inbound failed: ${(e as Error).message}`
      this._render()
    }
  }
  private async _createTrigger(body: Record<string, unknown>): Promise<void> {
    try {
      await this._send('POST', '/api/triggers', body)
      this._note = 'Trigger created.'
      await this._loadTriggers()
    } catch (e) {
      this._note = 'Create failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _deleteTrigger(name: string): Promise<void> {
    try {
      await this._send('DELETE', `/api/triggers/${encodeURIComponent(name)}`)
      await this._loadTriggers()
    } catch (e) {
      this._note = 'Delete failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _runTrigger(name: string): Promise<void> {
    this._note = `Firing ${name}…`
    this._render()
    try {
      const res = await this._send<{ content: string }>('POST', `/api/triggers/${encodeURIComponent(name)}/run`)
      this._note = `${name} ran: ${res.content?.slice(0, 200) || '(no output)'}`
    } catch (e) {
      this._note = `Run failed: ${(e as Error).message}`
    }
    this._render()
  }
  private async _resolveInbox(id: string, decision: string): Promise<void> {
    try {
      await this._send('POST', `/api/inbox/${encodeURIComponent(id)}/resolve`, { decision })
      await this._loadInbox()
    } catch (e) {
      this._note = 'Resolve failed: ' + (e as Error).message
      this._render()
    }
  }

  // Streams a chat reply (POST → ReadableStream, parsing SSE frames).
  private async _chat(text: string): Promise<void> {
    if (!this._selected || this._streaming) return
    this._messages.push({ role: 'user', content: text })
    const assistant: ChatMessage = { role: 'assistant', content: '' }
    this._messages.push(assistant)
    this._streaming = true
    this._error = null
    this._render()
    try {
      const r = await fetch(this._api(`/api/agents/${encodeURIComponent(this._selected)}/chat`), {
        method: 'POST',
        credentials: 'same-origin',
        headers: this._headers(true),
        body: JSON.stringify({ message: text }),
      })
      if (!r.ok || !r.body) throw new Error(`${r.status} ${(await r.json().catch(() => ({})))?.message || r.statusText}`)
      const reader = r.body.getReader()
      const dec = new TextDecoder()
      let buf = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
        const frames = buf.split('\n\n')
        buf = frames.pop() || ''
        for (const f of frames) {
          const ev = this._parseSSE(f)
          if (!ev) continue
          if (ev.event === 'delta' && ev.data?.text) {
            assistant.content += ev.data.text
            this._render()
          } else if (ev.event === 'tool' && ev.data?.name) {
            // Insert the tool row before the (streaming) assistant bubble so
            // the transcript reads: narration → tool → continued answer.
            const row: ChatMessage = {
              role: 'tool',
              error: !!ev.data.error,
              content: `${ev.data.name}(${ev.data.args || ''}) → ${ev.data.result || ''}`,
            }
            this._messages.splice(this._messages.length - 1, 0, row)
            this._render()
          } else if (ev.event === 'error') {
            this._error = ev.data?.message || 'stream error'
          }
        }
      }
    } catch (e) {
      const msg = (e as Error).message
      this._error = /not found|credentials|kedge-agents-llm|not configured/i.test(msg)
        ? 'No model configured — open the Settings tab to add one.'
        : 'Chat failed: ' + msg
    }
    this._streaming = false
    this._render()
  }
  private _parseSSE(frame: string): { event: string; data: any } | null {
    let event = 'message'
    let data = ''
    for (const line of frame.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data += line.slice(5).trim()
    }
    if (!data) return null
    try {
      return { event, data: JSON.parse(data) }
    } catch {
      return { event, data }
    }
  }

  // ---- rendering -----------------------------------------------------------

  private _render(): void {
    if (!this._ctx) {
      this.innerHTML = `<div class="agents-empty"><p class="muted">Connecting…</p></div>`
      return
    }
    if (!this._hasWorkspace()) {
      this.innerHTML = `<div class="agents-empty"><p class="muted">Select an organization and workspace in the sidebar to use your agents.</p></div>`
      return
    }
    const pendingInbox = this._inbox.filter((i) => i.state === 'pending').length
    const tabs: [Tab, string][] = [
      ['agents', 'Chat'],
      ['schedules', 'Schedules'],
      ['triggers', 'Triggers'],
      ['connections', 'Connections'],
      ['inbox', pendingInbox ? `Inbox (${pendingInbox})` : 'Inbox'],
      ['models', 'Models'],
    ]
    const body =
      this._tab === 'agents'
        ? this._renderAgents()
        : this._tab === 'schedules'
          ? this._renderSchedules()
          : this._tab === 'triggers'
            ? this._renderTriggers()
            : this._tab === 'connections'
              ? this._renderConnections()
              : this._tab === 'inbox'
                ? this._renderInbox()
                : this._renderModels()

    const hasCreds = this._credentials.length > 0
    this.innerHTML = `
      <div class="agents-app">
        <nav class="agents-nav">
          ${tabs.map(([id, label]) => `<button class="agents-navbtn ${this._tab === id ? 'sel' : ''}" data-tab="${id}">${label}</button>`).join('')}
          <span class="agents-nav-spacer"></span>
          <span class="agents-model ${hasCreds ? 'ok' : 'warn'}" data-tab="models">
            ${hasCreds ? `${this._credentials.length} model credential${this._credentials.length === 1 ? '' : 's'}` : 'No models — add one'}
          </span>
        </nav>
        ${this._note ? `<div class="agents-note" data-clear-note>${escapeHTML(this._note)}</div>` : ''}
        <div class="agents-view">${body}</div>
      </div>`

    this.querySelectorAll<HTMLElement>('[data-tab]').forEach((el) =>
      el.addEventListener('click', () => {
        this._tab = el.dataset.tab as Tab
        this._render()
      }),
    )
    this.querySelector<HTMLElement>('[data-clear-note]')?.addEventListener('click', () => {
      this._note = null
      this._render()
    })
    this._wireActive()
  }

  private _wireActive(): void {
    if (this._tab === 'agents') this._wireAgents()
    else if (this._tab === 'schedules') this._wireSchedules()
    else if (this._tab === 'triggers') this._wireTriggers()
    else if (this._tab === 'connections') this._wireConnections()
    else if (this._tab === 'inbox') this._wireInbox()
    else this._wireModels()
  }

  // Agents (chat) view.
  private _renderAgents(): string {
    const credOptions = (selected?: string) =>
      `<option value="">— no model —</option>` +
      this._credentials.map((c) => `<option value="${escapeHTML(c.name)}" ${c.name === selected ? 'selected' : ''}>${escapeHTML(c.name)}${c.model ? ` (${escapeHTML(c.model)})` : ''}</option>`).join('')
    const messaging = this._connections.filter((c) => ['telegram', 'slack', 'smtp'].includes(c.spec.type))
    const notifyOptions = (selected?: string) =>
      `<option value="">— no notify —</option>` +
      messaging.map((c) => `<option value="${escapeHTML(c.metadata.name)}" ${c.metadata.name === selected ? 'selected' : ''}>notify: ${escapeHTML(c.metadata.name)}</option>`).join('')
    return `
      <div class="agents-root">
        <aside class="agents-side">
          ${this._error ? `<div class="agents-err">${escapeHTML(this._error)}</div>` : ''}
          <ul class="agents-list">
            ${
              this._agents.length
                ? this._agents
                    .map(
                      (a) => `<li class="agents-item ${a.metadata.name === this._selected ? 'sel' : ''}" data-name="${escapeHTML(a.metadata.name)}">
                        <div class="agents-item-top">
                          <span class="agents-name">${escapeHTML(a.spec?.displayName || a.metadata.name)}</span>
                          <button class="agents-x" data-del="${escapeHTML(a.metadata.name)}" title="Delete">×</button>
                        </div>
                        <select class="agents-cred" data-reassign="${escapeHTML(a.metadata.name)}" title="Model credential">
                          ${credOptions(a.spec?.models?.chat)}
                        </select>
                        <select class="agents-cred" data-notify="${escapeHTML(a.metadata.name)}" title="Where background runs deliver output">
                          ${notifyOptions(a.spec?.defaultNotifyConnection)}
                        </select>
                      </li>`,
                    )
                    .join('')
                : `<li class="muted">No agents yet — create one below.</li>`
            }
          </ul>
          <form class="agents-create">
            <input name="name" placeholder="agent-id" required pattern="[a-z0-9-]+" />
            <select name="cred">${credOptions()}</select>
            <input name="budgetUSD" placeholder="budget $/mo (optional)" inputmode="decimal" />
            <button>Create</button>
          </form>
          ${this._credentials.length === 0 ? `<p class="muted" style="font-size:12px">Add a model on the <strong>Models</strong> tab, then assign it here.</p>` : ''}
        </aside>
        <main class="agents-main">${this._renderChat()}</main>
      </div>`
  }
  private _renderChat(): string {
    if (!this._selected) return `<div class="agents-empty"><p class="muted">Select or create an agent to chat.</p></div>`
    return `
      <div class="agents-chat">
        <div class="agents-chat-head"><h3>${escapeHTML(this._selected)}</h3></div>
        <div class="agents-log">
          ${
            this._messages.length
              ? this._messages
                  .map(
                    (m) =>
                      m.role === 'tool'
                        ? `<div class="agents-msg tool ${m.error ? 'err' : ''}"><div class="agents-toolrow">🔧 ${escapeHTML(m.content)}</div></div>`
                        : `<div class="agents-msg ${m.role}"><div class="agents-role">${m.role}</div><div class="agents-body">${escapeHTML(m.content) || (this._streaming && m.role === 'assistant' ? '…' : '')}</div></div>`,
                  )
                  .join('')
              : `<p class="muted">No messages yet. Say hi.</p>`
          }
        </div>
        <form class="agents-chat-form"><input placeholder="Message ${escapeHTML(this._selected)}…" ${this._streaming ? 'disabled' : ''} autocomplete="off" /><button ${this._streaming ? 'disabled' : ''}>${this._streaming ? '…' : 'Send'}</button></form>
      </div>`
  }
  private _wireAgents(): void {
    this.querySelectorAll<HTMLElement>('.agents-item').forEach((el) =>
      el.addEventListener('click', (e) => {
        const t = e.target as HTMLElement
        if (t.dataset.del || t.dataset.reassign || t.dataset.notify || t.closest('[data-reassign],[data-notify]')) return
        this._select(el.dataset.name!)
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-del]').forEach((el) =>
      el.addEventListener('click', (e) => {
        e.stopPropagation()
        if (confirm(`Delete agent ${el.dataset.del}?`)) void this._deleteAgent(el.dataset.del!)
      }),
    )
    this.querySelectorAll<HTMLSelectElement>('[data-reassign]').forEach((el) =>
      el.addEventListener('change', (e) => {
        e.stopPropagation()
        void this._reassignAgent(el.dataset.reassign!, el.value)
      }),
    )
    this.querySelectorAll<HTMLSelectElement>('[data-notify]').forEach((el) =>
      el.addEventListener('change', (e) => {
        e.stopPropagation()
        void this._setNotify(el.dataset.notify!, el.value)
      }),
    )
    const cf = this.querySelector<HTMLFormElement>('.agents-create')
    cf?.addEventListener('submit', (e) => {
      e.preventDefault()
      const v = cf.querySelector<HTMLInputElement>('input[name=name]')!.value.trim()
      const cred = cf.querySelector<HTMLSelectElement>('select[name=cred]')?.value || ''
      const budgetUSD = cf.querySelector<HTMLInputElement>('input[name=budgetUSD]')?.value.trim() || ''
      if (v) void this._createAgent(v, cred, budgetUSD)
    })
    const chat = this.querySelector<HTMLFormElement>('.agents-chat-form')
    chat?.addEventListener('submit', (e) => {
      e.preventDefault()
      const input = chat.querySelector<HTMLInputElement>('input')!
      const t = input.value.trim()
      if (t) {
        input.value = ''
        void this._chat(t)
      }
    })
    const log = this.querySelector<HTMLElement>('.agents-log')
    if (log) log.scrollTop = log.scrollHeight
  }

  // Schedules view.
  private _renderSchedules(): string {
    const agentOptions = this._agents.map((a) => `<option value="${escapeHTML(a.metadata.name)}">${escapeHTML(a.metadata.name)}</option>`).join('')
    return `
      <div class="agents-panel">
        <h3>Schedules</h3>
        <p class="muted">Cron and heartbeat schedules fire autonomously on the provider's clock (cadence ~30s); wakeups fire once at their time. Use <strong>Run now</strong> to test immediately. Set a <em>notify</em> connection on the agent to receive background output on Telegram/Slack/email.</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Agent</th><th>Type</th><th>Schedule</th><th>Next</th><th></th></tr></thead>
          <tbody>
            ${
              this._schedules.length
                ? this._schedules
                    .map(
                      (s) => `<tr>
                        <td>${escapeHTML(s.metadata.name)}${s.spec.suspend ? ' <span class="muted">(suspended)</span>' : ''}</td>
                        <td>${escapeHTML(s.spec.agentRef)}</td>
                        <td>${escapeHTML(s.spec.type)}</td>
                        <td><code>${escapeHTML(s.spec.schedule || '—')}</code> ${s.spec.timeZone ? escapeHTML(s.spec.timeZone) : ''}</td>
                        <td>${s.status?.nextRun ? escapeHTML(s.status.nextRun) : '—'}</td>
                        <td class="agents-row-actions">
                          <button data-run="${escapeHTML(s.metadata.name)}">Run now</button>
                          <button class="secondary" data-delsched="${escapeHTML(s.metadata.name)}">Delete</button>
                        </td>
                      </tr>`,
                    )
                    .join('')
                : `<tr><td colspan="6" class="muted">No schedules yet.</td></tr>`
            }
          </tbody>
        </table>
        <form class="agents-sched-form">
          <h4>New schedule</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="daily-digest" /></label>
            <label>Agent<select name="agentRef" required>${agentOptions || '<option value="">create an agent first</option>'}</select></label>
            <label>Type<select name="type"><option value="cron">cron</option><option value="heartbeat">heartbeat</option><option value="wakeup">wakeup</option></select></label>
            <label>Cron (5-field, UTC unless TZ)<input name="schedule" placeholder="0 8 * * *" /></label>
            <label>Time zone (IANA)<input name="timeZone" placeholder="Europe/Vilnius" /></label>
            <label>Run at (wakeup, RFC3339)<input name="runAt" placeholder="2026-07-13T09:00:00Z" /></label>
          </div>
          <label>Task / checklist<textarea name="task" rows="2" placeholder="Summarize today's open PRs and email me."></textarea></label>
          <button>Create schedule</button>
        </form>
      </div>`
  }
  private _wireSchedules(): void {
    this.querySelectorAll<HTMLElement>('[data-run]').forEach((el) => el.addEventListener('click', () => void this._runSchedule(el.dataset.run!)))
    this.querySelectorAll<HTMLElement>('[data-delsched]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete schedule ${el.dataset.delsched}?`)) void this._deleteSchedule(el.dataset.delsched!)
      }),
    )
    const f = this.querySelector<HTMLFormElement>('.agents-sched-form')
    f?.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
      const type = g('type')
      const body: Record<string, unknown> = { name: g('name'), agentRef: g('agentRef'), type, timeZone: g('timeZone') }
      if (type === 'wakeup') body.runAt = g('runAt')
      else body.schedule = g('schedule')
      if (type === 'heartbeat') body.checklist = g('task')
      else body.task = g('task')
      void this._createSchedule(body)
    })
  }

  // Connections view.
  private _renderConnections(): string {
    return `
      <div class="agents-panel">
        <h3>Connections</h3>
        <p class="muted">Named credentials for external systems. Tool families and channels light up per agent from these; the secret lives in a Secret in your workspace. For Telegram/Slack, <strong>Inbound</strong> lets you chat with an agent from the channel — messages route to the agent whose <em>notify</em> dropdown points at that connection (commands: <code>/new</code>, <code>/status</code>).</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Type</th><th>Endpoint / channel</th><th></th></tr></thead>
          <tbody>
            ${
              this._connections.length
                ? this._connections
                    .map(
                      (c) => `<tr>
                        <td>${escapeHTML(c.spec.displayName || c.metadata.name)}</td>
                        <td>${escapeHTML(c.spec.type)}${c.status?.webhookPath ? ` <span class="agents-inbound-on" title="Inbound enabled">⇄</span>` : ''}${c.status?.oauthConnected ? ` <span class="agents-inbound-on" title="OAuth connected">🔗</span>` : ''}</td>
                        <td>${escapeHTML(c.spec.baseURL || c.spec.channel || '—')}${c.status?.webhookPath ? `<div class="agents-webhook"><code title="Platform events POST here">${escapeHTML(c.status.webhookPath)}</code></div>` : ''}</td>
                        <td class="agents-row-actions">${
                          c.spec.auth === 'oauth' ? `<button data-oauth="${escapeHTML(c.metadata.name)}" title="Authorize with the provider">Connect</button>` : ''
                        }${
                          ['telegram', 'slack'].includes(c.spec.type)
                            ? `<button data-inbound="${escapeHTML(c.metadata.name)}" title="Chat with the bound agent from this channel">Inbound</button>`
                            : ''
                        }${['telegram', 'slack', 'smtp'].includes(c.spec.type) ? `<button data-testconn="${escapeHTML(c.metadata.name)}">Test</button>` : ''}<button class="secondary" data-delconn="${escapeHTML(c.metadata.name)}">Delete</button></td>
                      </tr>`,
                    )
                    .join('')
                : `<tr><td colspan="4" class="muted">No connections yet.</td></tr>`
            }
          </tbody>
        </table>
        <form class="agents-conn-form">
          <h4>New connection</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="my-github" /></label>
            <label>Type<select name="type">${CONNECTION_TYPES.map((t) => `<option value="${t}">${t}</option>`).join('')}</select></label>
            <label>Base URL / endpoint<input name="baseURL" placeholder="https://api.githubcopilot.com/mcp" /></label>
            <label>Channel / chat id<input name="channel" placeholder="(telegram/slack/smtp)" /></label>
            <label>Auth<select name="auth"><option value="secret">Token (paste)</option><option value="oauth">OAuth app (Connect flow)</option></select></label>
            <label class="agents-oauth-only" hidden>OAuth provider<select name="oauthProvider"><option value="github">github</option><option value="google">google</option><option value="slack">slack</option></select></label>
          </div>
          <label class="agents-secret-only">Secret (token / API key / bot token)<input name="secret" type="password" autocomplete="off" placeholder="stored in kedge-agents-conn-<name>" /></label>
          <div class="agents-grid2 agents-oauth-only" hidden>
            <label>Client ID<input name="clientID" autocomplete="off" /></label>
            <label>Client secret<input name="clientSecret" type="password" autocomplete="off" /></label>
          </div>
          <button>Create connection</button>
        </form>
      </div>`
  }
  private _wireConnections(): void {
    this.querySelectorAll<HTMLElement>('[data-delconn]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete connection ${el.dataset.delconn}?`)) void this._deleteConnection(el.dataset.delconn!)
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-testconn]').forEach((el) => el.addEventListener('click', () => void this._testConnection(el.dataset.testconn!)))
    this.querySelectorAll<HTMLElement>('[data-inbound]').forEach((el) => el.addEventListener('click', () => void this._enableInbound(el.dataset.inbound!)))
    this.querySelectorAll<HTMLElement>('[data-oauth]').forEach((el) => el.addEventListener('click', () => void this._oauthConnect(el.dataset.oauth!)))
    const f = this.querySelector<HTMLFormElement>('.agents-conn-form')
    if (!f) return
    const authSel = f.querySelector<HTMLSelectElement>('select[name=auth]')!
    const syncAuth = () => {
      const oauth = authSel.value === 'oauth'
      f.querySelectorAll<HTMLElement>('.agents-oauth-only').forEach((el) => (el.hidden = !oauth))
      f.querySelectorAll<HTMLElement>('.agents-secret-only').forEach((el) => (el.hidden = oauth))
    }
    authSel.addEventListener('change', syncAuth)
    syncAuth()
    f.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement>(`[name=${n}]`)?.value || '').trim()
      void this._createConnection({
        name: g('name'), type: g('type'), baseURL: g('baseURL'), channel: g('channel'),
        secret: g('secret'), auth: g('auth'),
        oauthProvider: g('oauthProvider'), clientID: g('clientID'), clientSecret: g('clientSecret'),
      })
    })
  }

  // Settings (model credentials) view.
  // Triggers view (event-driven automation).
  private _renderTriggers(): string {
    const agentOptions = this._agents.map((a) => `<option value="${escapeHTML(a.metadata.name)}">${escapeHTML(a.metadata.name)}</option>`).join('')
    const connOptions = `<option value="">— none —</option>` + this._connections.map((c) => `<option value="${escapeHTML(c.metadata.name)}">${escapeHTML(c.metadata.name)} (${escapeHTML(c.spec.type)})</option>`).join('')
    return `
      <div class="agents-panel">
        <h3>Triggers</h3>
        <p class="muted">Run an agent when an event happens ("when X, do Y"). Webhook triggers get a hub-routed URL; channel/github triggers subscribe through a connection. Use <strong>Fire</strong> to test with an optional payload.</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Agent</th><th>Source</th><th>Connection</th><th></th></tr></thead>
          <tbody>
            ${
              this._triggers.length
                ? this._triggers
                    .map(
                      (t) => `<tr>
                        <td>${escapeHTML(t.metadata.name)}${t.spec.suspend ? ' <span class="muted">(suspended)</span>' : ''}</td>
                        <td>${escapeHTML(t.spec.agentRef)}</td>
                        <td>${escapeHTML(t.spec.source)}${t.status?.webhookPath ? `<div class="agents-webhook"><code title="POST an event payload here">${escapeHTML(t.status.webhookPath)}</code></div>` : ''}</td>
                        <td>${escapeHTML(t.spec.connectionRef || '—')}</td>
                        <td class="agents-row-actions"><button data-firetrig="${escapeHTML(t.metadata.name)}">Fire</button><button class="secondary" data-deltrig="${escapeHTML(t.metadata.name)}">Delete</button></td>
                      </tr>`,
                    )
                    .join('')
                : `<tr><td colspan="5" class="muted">No triggers yet.</td></tr>`
            }
          </tbody>
        </table>
        <form class="agents-trig-form">
          <h4>New trigger</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="on-issue" /></label>
            <label>Agent<select name="agentRef" required>${agentOptions || '<option value="">create an agent first</option>'}</select></label>
            <label>Source<select name="source"><option value="webhook">webhook</option><option value="github">github</option><option value="channel">channel</option><option value="connection">connection</option></select></label>
            <label>Connection<select name="connectionRef">${connOptions}</select></label>
          </div>
          <label>Task<textarea name="task" rows="2" placeholder="Triage the incoming GitHub issue and label it."></textarea></label>
          <button>Create trigger</button>
        </form>
      </div>`
  }
  private _wireTriggers(): void {
    this.querySelectorAll<HTMLElement>('[data-firetrig]').forEach((el) => el.addEventListener('click', () => void this._runTrigger(el.dataset.firetrig!)))
    this.querySelectorAll<HTMLElement>('[data-deltrig]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete trigger ${el.dataset.deltrig}?`)) void this._deleteTrigger(el.dataset.deltrig!)
      }),
    )
    const f = this.querySelector<HTMLFormElement>('.agents-trig-form')
    f?.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
      void this._createTrigger({ name: g('name'), agentRef: g('agentRef'), source: g('source'), connectionRef: g('connectionRef'), task: g('task') })
    })
  }

  // Inbox view (approvals + questions).
  private _renderInbox(): string {
    return `
      <div class="agents-panel">
        <h3>Inbox</h3>
        <p class="muted">Approvals and questions your agents raise land here (and on your channels). Populated once agents run tools that need sign-off.</p>
        <table class="agents-table">
          <thead><tr><th>Agent</th><th>Kind</th><th>Prompt</th><th>State</th><th></th></tr></thead>
          <tbody>
            ${
              this._inbox.length
                ? this._inbox
                    .map(
                      (i) => `<tr>
                        <td>${escapeHTML(i.agentName)}</td>
                        <td>${escapeHTML(i.kind)}</td>
                        <td>${escapeHTML(i.prompt)}</td>
                        <td>${escapeHTML(i.state)}</td>
                        <td class="agents-row-actions">${
                          i.state === 'pending'
                            ? `<button data-approve="${escapeHTML(i.id)}">Approve</button><button class="secondary" data-deny="${escapeHTML(i.id)}">Deny</button>`
                            : ''
                        }</td>
                      </tr>`,
                    )
                    .join('')
                : `<tr><td colspan="5" class="muted">Nothing needs your attention.</td></tr>`
            }
          </tbody>
        </table>
      </div>`
  }
  private _wireInbox(): void {
    this.querySelectorAll<HTMLElement>('[data-approve]').forEach((el) => el.addEventListener('click', () => void this._resolveInbox(el.dataset.approve!, 'approve')))
    this.querySelectorAll<HTMLElement>('[data-deny]').forEach((el) => el.addEventListener('click', () => void this._resolveInbox(el.dataset.deny!, 'deny')))
  }

  // Models (named credentials) view: create once, assign to agents.
  private _renderModels(): string {
    return `
      <div class="agents-panel agents-settings">
        <h3>Model credentials</h3>
        <p class="muted">Create reusable credentials (OpenAI, Anthropic, …) and assign them to agents on the Chat tab. Each is stored as its own Secret (<code>kedge-agents-model-&lt;name&gt;</code>) in your workspace, so you can keep several and share them.</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Provider</th><th>Model</th><th>Base URL</th><th></th></tr></thead>
          <tbody>
            ${
              this._credentials.length
                ? this._credentials
                    .map(
                      (c) => `<tr>
                        <td><strong>${escapeHTML(c.name)}</strong></td>
                        <td>${escapeHTML(c.provider || '')}</td>
                        <td>${escapeHTML(c.model || '')}</td>
                        <td class="muted">${escapeHTML(c.baseURL || '')}</td>
                        <td class="agents-row-actions"><button class="secondary" data-delcred="${escapeHTML(c.name)}">Delete</button></td>
                      </tr>`,
                    )
                    .join('')
                : `<tr><td colspan="5" class="muted">No credentials yet — add one below.</td></tr>`
            }
          </tbody>
        </table>
        <form class="agents-cred-form">
          <h4>New credential</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="my-openai" /></label>
            <label>Provider<select name="preset">${PROVIDER_PRESETS.map((p) => `<option value="${p.id}">${escapeHTML(p.label)}</option>`).join('')}</select></label>
            <label>Base URL<input name="baseURL" value="${PROVIDER_PRESETS[0].baseURL}" placeholder="https://api.openai.com/v1" /></label>
            <label>Model<input name="model" placeholder="gpt-4o" required /></label>
          </div>
          <label>API key<input name="apiKey" type="password" autocomplete="off" placeholder="sk-…" required /></label>
          ${this._modelsMsg ? `<div class="agents-msg-note">${escapeHTML(this._modelsMsg)}</div>` : ''}
          <button>Add credential</button>
        </form>
      </div>`
  }
  private _wireModels(): void {
    this.querySelectorAll<HTMLElement>('[data-delcred]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete credential ${el.dataset.delcred}? Agents using it will need reassigning.`)) void this._deleteCredential(el.dataset.delcred!)
      }),
    )
    const form = this.querySelector<HTMLFormElement>('.agents-cred-form')
    if (!form) return
    const preset = form.querySelector<HTMLSelectElement>('select[name=preset]')!
    const baseURL = form.querySelector<HTMLInputElement>('input[name=baseURL]')!
    const model = form.querySelector<HTMLInputElement>('input[name=model]')!
    preset.addEventListener('change', () => {
      const p = PROVIDER_PRESETS.find((x) => x.id === preset.value)
      if (p && p.id !== 'custom') baseURL.value = p.baseURL
      if (p) model.placeholder = p.modelHint
    })
    form.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (form.querySelector<HTMLInputElement>(`input[name=${n}]`)?.value || '').trim()
      void this._createCredential({ name: g('name'), provider: 'openai-compatible', baseURL: baseURL.value.trim(), model: model.value.trim(), apiKey: g('apiKey') })
    })
  }
}

function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string)
}
