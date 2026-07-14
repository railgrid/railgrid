// AgentsElement is the custom element the kedge portal renders for the agents
// provider. The portal loads main.js (registering this element), appends the
// element, sets element.kedgeContext as a JS property, and listens for bubbled
// events. The element runs in light DOM so the portal's CSS variables cascade.
//
// Information architecture is AGENT-FIRST (like app-studio projects): the left
// sidebar lists agents (the starting point); selecting one opens its own
// Chat / Schedules / Triggers / Channels / Settings. Models (credentials),
// Connections, and the Inbox are workspace-shared resources reached from the
// sidebar footer — they live outside any single agent.

import { FlowCanvas } from './flow'
import type { FlowModel, FNode, FWire, FlowCallbacks, DraftSpec } from './flow'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

interface Agent {
  metadata: { name: string }
  spec: {
    displayName?: string
    description?: string
    systemPrompt?: string
    autonomy?: string
    models?: Record<string, string>
    defaultNotifyConnection?: string
    delegates?: string[]
    budget?: { window?: string; usdLimit?: string; tokenLimit?: number }
    tools?: { interactive?: { families?: string[]; toolsets?: string[] }; background?: { families?: string[]; toolsets?: string[] } }
  }
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
  spec: { agentRef: string; type: string; schedule?: string; runAt?: string; timeZone?: string; task?: string; checklist?: string; suspend?: boolean }
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
interface Toolset {
  metadata: { name: string }
  spec: { displayName?: string; description?: string; families?: string[]; connections?: string[]; requireApproval?: string[] }
  status?: { usedBy?: number }
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

type AgentTab = 'chat' | 'schedules' | 'triggers' | 'channels' | 'settings'
type SharedView = 'models' | 'connections' | 'inbox'

const PROVIDER_PRESETS: { id: string; label: string; baseURL: string; modelHint: string }[] = [
  { id: 'openai', label: 'OpenAI', baseURL: 'https://api.openai.com/v1', modelHint: 'gpt-4o' },
  { id: 'anthropic', label: 'Anthropic (Claude, OpenAI-compat)', baseURL: 'https://api.anthropic.com/v1', modelHint: 'claude-sonnet-4-20250514' },
  { id: 'openrouter', label: 'OpenRouter', baseURL: 'https://openrouter.ai/api/v1', modelHint: 'anthropic/claude-sonnet-4' },
  { id: 'custom', label: 'Custom (OpenAI-compatible)', baseURL: '', modelHint: 'model-name' },
]
// Connection creation is TYPE-DRIVEN: pick what you're connecting, then a form
// with only that connection's fields, each labelled with where to get it.
interface ConnField {
  key: string
  label: string
  hint?: string
  placeholder?: string
  password?: boolean
  required?: boolean
}
interface ConnMode {
  id: string
  label: string
  fields: ConnField[]
}
interface ConnTypeDef {
  id: string
  label: string
  glyph: string
  desc: string
  // setup is an ordered list of setup steps (HTML allowed), shown as a guide at
  // the top of the create form so users know what to prepare.
  setup?: string[]
  fields?: ConnField[]
  modes?: ConnMode[]
  advanced?: ConnField[]
  build: (v: Record<string, string>, mode: string) => Record<string, unknown>
}

// Connections fall into three kinds so the UI can label what each one is FOR:
//  - tool:       a capability agents call during a run (GitHub, MCP, web search)
//  - channel:    where agents message you (Telegram, Slack, Discord, email)
//  - connection: a generic API credential for custom integrations (HTTP)
type ConnCategory = 'tool' | 'channel' | 'connection'
const CONN_CATEGORY: Record<string, ConnCategory> = {
  github: 'tool',
  mcp: 'tool',
  websearch: 'tool',
  telegram: 'channel',
  slack: 'channel',
  discord: 'channel',
  'discord-webhook': 'channel',
  smtp: 'channel',
  http: 'connection',
}
const CATEGORY_META: Record<ConnCategory, { icon: string; label: string; blurb: string }> = {
  tool: { icon: '🔧', label: 'Tool', blurb: 'Capabilities agents call during a run.' },
  channel: { icon: '📣', label: 'Channel', blurb: 'Where agents message you — notify + inbound chat.' },
  connection: { icon: '🔌', label: 'Connection', blurb: 'Generic API credentials for custom integrations.' },
}
function connCategory(id: string): ConnCategory {
  return CONN_CATEGORY[id] || 'connection'
}

const CONN_DEFS: ConnTypeDef[] = [
  {
    id: 'github',
    label: 'GitHub',
    glyph: '🐙',
    desc: 'Issues, PRs, code search via the GitHub MCP server',
    modes: [
      {
        id: 'pat',
        label: 'Access token',
        fields: [{ key: 'token', label: 'Personal access token', password: true, required: true, hint: 'Create at github.com/settings/tokens — grant repo (and read:org for org access).' }],
      },
      {
        id: 'oauth',
        label: 'OAuth app',
        fields: [
          { key: 'clientID', label: 'Client ID', required: true, hint: 'From your GitHub OAuth App (Settings → Developer settings → OAuth Apps).' },
          { key: 'clientSecret', label: 'Client secret', password: true, required: true },
          { key: 'scopes', label: 'Scopes', placeholder: 'repo read:org' },
        ],
      },
    ],
    advanced: [{ key: 'baseURL', label: 'MCP endpoint (GitHub Enterprise only)', placeholder: 'https://api.githubcopilot.com/mcp' }],
    build: (v, mode) => {
      const b: Record<string, unknown> = { type: 'github', name: v.name }
      if (v.baseURL) b.baseURL = v.baseURL
      if (mode === 'oauth') {
        b.auth = 'oauth'
        b.oauthProvider = 'github'
        b.clientID = v.clientID
        b.clientSecret = v.clientSecret
        if (v.scopes) b.oauthScopes = v.scopes.trim().split(/\s+/)
      } else b.secret = v.token
      return b
    },
  },
  {
    id: 'mcp',
    label: 'MCP server',
    glyph: '🧩',
    desc: 'Any Model Context Protocol server',
    fields: [
      { key: 'baseURL', label: 'Server endpoint', required: true, placeholder: 'https://example.com/mcp', hint: 'The server’s streamable-HTTP MCP URL.' },
      { key: 'token', label: 'Bearer token', password: true, hint: 'Only if the server requires authentication.' },
    ],
    build: (v) => ({ type: 'mcp', name: v.name, baseURL: v.baseURL, secret: v.token || undefined }),
  },
  {
    id: 'websearch',
    label: 'Web search',
    glyph: '🔎',
    desc: 'Give agents web_search (Brave-compatible API)',
    fields: [{ key: 'token', label: 'API key', password: true, required: true, hint: 'Brave Search API key — api.search.brave.com/app/keys (free tier available).' }],
    advanced: [{ key: 'baseURL', label: 'Custom endpoint', placeholder: 'https://api.search.brave.com/res/v1/web/search' }],
    build: (v) => ({ type: 'websearch', name: v.name, secret: v.token, baseURL: v.baseURL || undefined }),
  },
  {
    id: 'telegram',
    label: 'Telegram',
    glyph: '✈️',
    desc: 'Notify + chat with your agent on Telegram',
    fields: [
      { key: 'token', label: 'Bot token', password: true, required: true, hint: 'Create a bot with @BotFather — it gives a token like 12345:ABC…' },
      { key: 'channel', label: 'Chat ID', required: true, hint: 'Your numeric chat id — message @userinfobot to get it (or a group id).' },
    ],
    build: (v) => ({ type: 'telegram', name: v.name, secret: v.token, channel: v.channel }),
  },
  {
    id: 'slack',
    label: 'Slack',
    glyph: '💬',
    desc: 'Notify + chat in Slack',
    modes: [
      {
        id: 'bot',
        label: 'Bot token',
        fields: [
          { key: 'token', label: 'Bot token', password: true, required: true, hint: 'xoxb-… from your Slack app → OAuth & Permissions. Needs chat:write.' },
          { key: 'channel', label: 'Channel ID', required: true, hint: 'e.g. C0123ABC — channel → View details → bottom.' },
        ],
      },
      {
        id: 'webhook',
        label: 'Incoming webhook',
        fields: [{ key: 'channel', label: 'Webhook URL', required: true, hint: 'https://hooks.slack.com/services/… — outbound notify only, no inbound chat.' }],
      },
      {
        id: 'oauth',
        label: 'OAuth app',
        fields: [
          { key: 'clientID', label: 'Client ID', required: true },
          { key: 'clientSecret', label: 'Client secret', password: true, required: true },
          { key: 'scopes', label: 'Scopes', placeholder: 'chat:write channels:history' },
          { key: 'channel', label: 'Channel ID', required: true },
        ],
      },
    ],
    build: (v, mode) => {
      const b: Record<string, unknown> = { type: 'slack', name: v.name }
      if (mode === 'webhook') b.channel = v.channel
      else if (mode === 'oauth') {
        b.auth = 'oauth'
        b.oauthProvider = 'slack'
        b.clientID = v.clientID
        b.clientSecret = v.clientSecret
        b.channel = v.channel
        if (v.scopes) b.oauthScopes = v.scopes.trim().split(/\s+/)
      } else {
        b.secret = v.token
        b.channel = v.channel
      }
      return b
    },
  },
  {
    id: 'discord',
    label: 'Discord chat',
    glyph: '🎮',
    desc: 'Two-way chat with your agent (bot)',
    setup: [
      'Create the bot: <a href="https://discord.com/developers/applications" target="_blank" rel="noopener">Discord Developer Portal</a> → <strong>New Application</strong> → <strong>Bot</strong> → <strong>Reset Token</strong> → copy it into <strong>Bot token</strong> below.',
      'Enable reading messages: on that same <strong>Bot</strong> page, turn ON <strong>MESSAGE CONTENT INTENT</strong> (privileged). Without it the bot can’t see what you type.',
      'Invite it to your server: <strong>OAuth2 → URL Generator</strong> → scope <code>bot</code> → permissions <strong>View Channel</strong>, <strong>Send Messages</strong>, <strong>Read Message History</strong> → open the generated URL and add the bot. (Missing these → 403 on send.)',
      '(Optional) Home channel: enable <strong>Developer Mode</strong> (User Settings → Advanced), right-click a text channel → <strong>Copy ID</strong> → paste below. Needed if you want notify/scheduled output delivered to Discord.',
      'Chat: <strong>DM the bot</strong> or <strong>@-mention</strong> it in any channel it can see. With a home channel set, it also replies there without a mention.',
    ],
    fields: [
      { key: 'token', label: 'Bot token', password: true, required: true, hint: 'From the Bot page → Reset Token (step 1).' },
      { key: 'channel', label: 'Home channel ID', hint: 'Right-click a channel → Copy ID. The bot auto-replies here (no @-mention) and scheduled/notify output is delivered here. Blank still works for chat — it replies to DMs and @-mentions in any channel — but leave it set if you want this agent to notify you.' },
    ],
    build: (v) => ({ type: 'discord', name: v.name, secret: v.token, channel: v.channel || undefined }),
  },
  {
    id: 'discord-webhook',
    label: 'Discord webhook',
    glyph: '📣',
    desc: 'Notify a Discord channel (outbound only)',
    fields: [
      { key: 'channel', label: 'Webhook URL', required: true, hint: 'Channel → Edit Channel → Integrations → Webhooks → New Webhook → Copy URL. Outbound only, no chat.' },
    ],
    build: (v) => ({ type: 'discord', name: v.name, channel: v.channel }),
  },
  {
    id: 'smtp',
    label: 'Email (SMTP)',
    glyph: '✉️',
    desc: 'Send email notifications',
    fields: [
      { key: 'host', label: 'SMTP host', required: true, placeholder: 'smtp.gmail.com' },
      { key: 'port', label: 'Port', placeholder: '587' },
      { key: 'from', label: 'From address', required: true, placeholder: 'agent@example.com' },
      { key: 'username', label: 'Username', placeholder: '(defaults to the From address)' },
      { key: 'token', label: 'Password', password: true, required: true, hint: 'SMTP password or an app password.' },
      { key: 'channel', label: 'Send to', required: true, placeholder: 'you@example.com' },
    ],
    build: (v) => ({ type: 'smtp', name: v.name, secret: v.token, channel: v.channel, config: { host: v.host, port: v.port || '', from: v.from, username: v.username || '' } }),
  },
  {
    id: 'http',
    label: 'HTTP API',
    glyph: '🌐',
    desc: 'Generic HTTP endpoint',
    fields: [
      { key: 'baseURL', label: 'Base URL', required: true, placeholder: 'https://api.example.com' },
      { key: 'token', label: 'Bearer token', password: true },
    ],
    build: (v) => ({ type: 'http', name: v.name, baseURL: v.baseURL, secret: v.token || undefined }),
  },
]

const AGENT_TABS: [AgentTab, string][] = [
  ['chat', 'Chat'],
  ['schedules', 'Schedules'],
  ['triggers', 'Triggers'],
  ['channels', 'Channels'],
  ['settings', 'Settings'],
]

export class AgentsElement extends HTMLElement {
  private _ctx: KedgeContext | null = null
  private _selected: string | null = null
  private _agentTab: AgentTab = 'chat'
  private _shared: SharedView | null = null
  private _agents: Agent[] = []
  private _schedules: Schedule[] = []
  private _connections: Connection[] = []
  private _triggers: Trigger[] = []
  private _toolsets: Toolset[] = []
  private _inbox: InboxItem[] = []
  private _credentials: Credential[] = []
  private _connType: string | null = null
  private _connMode = ''
  private _connEdit: string | null = null
  private _oauthApps = new Set<string>()
  private _trigEdit: string | null = null
  private _schedEdit: string | null = null
  private _schedType = 'cron'
  private _schedFreq = 'daily'
  private _messages: ChatMessage[] = []
  private _streaming = false
  private _error: string | null = null
  private _note: string | null = null
  private _modelsMsg: string | null = null
  private _loadedTenant: string | null = null
  private _agentView: 'flow' | 'list' = 'flow'
  private _flow: FlowCanvas | null = null
  private _flowRoot: HTMLElement | null = null

  set kedgeContext(v: KedgeContext | null) {
    this._ctx = v
    this._render()
    this._maybeLoad()
  }
  get kedgeContext(): KedgeContext | null {
    return this._ctx
  }
  private _onHashChange = (): void => {
    this._applyRoute()
    this._render()
  }
  connectedCallback(): void {
    this._applyRoute()
    window.addEventListener('hashchange', this._onHashChange)
    this._render()
    this._maybeLoad()
  }
  disconnectedCallback(): void {
    window.removeEventListener('hashchange', this._onHashChange)
  }

  private _maybeLoad(): void {
    if (!this._ctx?.basePath || !this._hasWorkspace()) return
    const key = this._ctx.tenant || JSON.stringify(this._tenant())
    if (key === this._loadedTenant) return
    // Switching tenants (not the first load) resets to home so we never show a
    // stale agent from another workspace. On first load we keep whatever the
    // hash route restored, so a refresh stays put.
    if (this._loadedTenant !== null) {
      this._selected = null
      this._shared = null
      if (location.hash && location.hash !== '#/') {
        try {
          history.replaceState(null, '', '#/')
        } catch {
          /* ignore */
        }
      }
    }
    this._loadedTenant = key
    this._messages = []
    void this._loadAgents()
    void this._loadCredentials()
    void this._loadConnections()
    void this._loadToolsets()
    void this._loadSchedules()
    void this._loadTriggers()
    void this._loadInbox()
    void this._loadOAuthApps()
  }
  private async _loadOAuthApps(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      const res = await this._get<{ providers?: Record<string, boolean> }>('/api/oauth/providers')
      const next = new Set(Object.entries(res.providers || {}).filter(([, v]) => v).map(([k]) => k))
      const changed = next.size !== this._oauthApps.size || [...next].some((p) => !this._oauthApps.has(p))
      this._oauthApps = next
      // Re-render so an already-open connection form drops the client id/secret
      // fields now that we know a platform app exists (the fetch is async).
      if (changed) this._render()
    } catch {
      /* optional — falls back to BYO client id/secret */
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
      this._schedules = (await this._get<{ items?: Schedule[] }>('/api/schedules')).items || []
    } catch {
      /* view shows its own empty state */
    }
    this._render()
  }
  private async _loadTriggers(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      this._triggers = (await this._get<{ items?: Trigger[] }>('/api/triggers')).items || []
    } catch {
      /* non-fatal */
    }
    this._render()
  }
  private async _loadConnections(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      this._connections = (await this._get<{ items?: Connection[] }>('/api/connections')).items || []
    } catch {
      /* non-fatal */
    }
    this._render()
  }
  private async _loadToolsets(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      this._toolsets = (await this._get<{ items?: Toolset[] }>('/api/toolsets')).items || []
    } catch {
      /* backend may predate toolsets — non-fatal */
    }
    this._render()
  }
  private async _loadCredentials(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      this._credentials = (await this._get<{ items?: Credential[] }>('/api/credentials')).items || []
      this._render()
    } catch {
      /* models view can still create the first credential */
    }
  }
  private async _loadInbox(): Promise<void> {
    if (!this._hasWorkspace()) return
    try {
      this._inbox = (await this._get<{ items?: InboxItem[] }>('/api/inbox')).items || []
      this._render()
    } catch {
      /* non-fatal */
    }
  }

  // ---- selection / navigation ----------------------------------------------

  private _agent(): Agent | undefined {
    return this._agents.find((a) => a.metadata.name === this._selected)
  }
  private _selectAgent(name: string): void {
    this._selected = name
    this._shared = null
    this._agentTab = 'chat'
    this._trigEdit = null
    this._schedEdit = null
    this._schedType = 'cron'
    this._schedFreq = 'daily'
    this._messages = []
    this._error = null
    this._render()
  }
  private _openShared(v: SharedView): void {
    this._shared = v
    this._selected = null
    this._render()
  }
  private _goHome(): void {
    this._selected = null
    this._shared = null
    this._render()
  }

  // ---- hash routing --------------------------------------------------------
  // The portal is an embedded micro-frontend, so we route via location.hash
  // (never sent to the host server): #/, #/connections, #/models, #/inbox,
  // #/agent/<name>/<tab>. The hash mirrors state on every render (replaceState,
  // which does NOT fire hashchange, so no loop) and is restored on load and on
  // browser back/forward — a refresh keeps you where you were.
  private _hashFor(): string {
    if (this._selected) return `#/agent/${encodeURIComponent(this._selected)}/${this._agentTab}`
    if (this._shared) return `#/${this._shared}`
    return '#/'
  }
  private _syncHash(): void {
    const h = this._hashFor()
    if (location.hash !== h) {
      try {
        history.replaceState(null, '', h)
      } catch {
        /* sandboxed iframe without same-origin history — ignore */
      }
    }
  }
  private _applyRoute(): void {
    const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean)
    if (parts[0] === 'agent' && parts[1]) {
      this._selected = decodeURIComponent(parts[1])
      this._shared = null
      const tab = parts[2] as AgentTab
      this._agentTab = AGENT_TABS.some(([id]) => id === tab) ? tab : 'chat'
    } else if (parts[0] === 'models' || parts[0] === 'connections' || parts[0] === 'inbox') {
      this._shared = parts[0]
      this._selected = null
    } else {
      this._selected = null
      this._shared = null
    }
  }

  // ---- agent actions -------------------------------------------------------

  private async _createAgent(name: string, modelCredential: string): Promise<void> {
    try {
      const body: Record<string, unknown> = { name, displayName: name }
      if (modelCredential) body.modelCredential = modelCredential
      await this._send('POST', '/api/agents', body)
      await this._loadAgents()
      this._selectAgent(name)
    } catch (e) {
      this._error = 'Create failed: ' + (e as Error).message
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
  private async _updateAgent(patch: Record<string, unknown>, note = 'Saved.'): Promise<void> {
    if (!this._selected) return
    try {
      await this._send('PUT', `/api/agents/${encodeURIComponent(this._selected)}`, patch)
      this._note = note
      await this._loadAgents()
    } catch (e) {
      this._note = 'Save failed: ' + (e as Error).message
      this._render()
    }
  }

  // ---- shared-resource actions ---------------------------------------------

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
  private async _updateConnection(name: string, patch: Record<string, unknown>): Promise<void> {
    try {
      await this._send('PUT', `/api/connections/${encodeURIComponent(name)}`, patch)
      this._note = 'Connection updated.'
      this._connEdit = null
      await this._loadConnections()
    } catch (e) {
      this._note = 'Update failed: ' + (e as Error).message
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
      const res = await this._send<{ authorizeURL: string }>('POST', `/api/connections/${encodeURIComponent(name)}/oauth/authorize`, {
        publicBaseURL: location.origin,
      })
      window.open(res.authorizeURL, '_blank', 'noopener')
      this._note = 'Authorize in the opened tab, then refresh.'
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
      const res = await this._send<{ webhookURL: string; registered: boolean; note: string }>('POST', `/api/connections/${encodeURIComponent(name)}/enable-inbound`, {
        publicBaseURL: location.origin,
      })
      this._note = `${res.registered ? '✅' : 'ℹ️'} ${res.note} URL: ${res.webhookURL}`
      await this._loadConnections()
    } catch (e) {
      this._note = `Enable inbound failed: ${(e as Error).message}`
      this._render()
    }
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

  // ---- schedule / trigger actions (agent-scoped) ---------------------------

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
  private async _updateSchedule(name: string, patch: Record<string, unknown>, note = 'Schedule updated.'): Promise<void> {
    try {
      await this._send('PUT', `/api/schedules/${encodeURIComponent(name)}`, patch)
      this._note = note
      this._schedEdit = null
      await this._loadSchedules()
    } catch (e) {
      this._note = 'Update failed: ' + (e as Error).message
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
  private async _updateTrigger(name: string, patch: Record<string, unknown>, note = 'Trigger updated.'): Promise<void> {
    try {
      await this._send('PUT', `/api/triggers/${encodeURIComponent(name)}`, patch)
      this._note = note
      this._trigEdit = null
      await this._loadTriggers()
    } catch (e) {
      this._note = 'Update failed: ' + (e as Error).message
      this._render()
    }
  }

  // ---- chat (agent-scoped, streaming) --------------------------------------

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
            const row: ChatMessage = { role: 'tool', error: !!ev.data.error, content: `${ev.data.name}(${ev.data.args || ''}) → ${ev.data.result || ''}` }
            this._messages.splice(this._messages.length - 1, 0, row)
            this._render()
          } else if (ev.event === 'error') {
            this._error = ev.data?.message || 'stream error'
          }
        }
      }
    } catch (e) {
      const msg = (e as Error).message
      this._error = /not found|credentials|kedge-agents-model|not configured/i.test(msg)
        ? 'No model configured — assign one in Settings.'
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

  // ---- rendering: shell ----------------------------------------------------

  private _render(): void {
    if (!this._ctx) {
      this.innerHTML = `<div class="agents-empty"><p class="muted">Connecting…</p></div>`
      return
    }
    if (!this._hasWorkspace()) {
      this.innerHTML = `<div class="agents-empty"><p class="muted">Select an organization and workspace in the sidebar to use your agents.</p></div>`
      return
    }
    this._syncHash()

    let inner: string
    if (this._shared) {
      const panel = this._shared === 'models' ? this._renderModels() : this._shared === 'connections' ? this._renderConnections() : this._renderInbox()
      inner = `<div class="agents-page">${this._renderBack()}${panel}</div>`
    } else if (this._selected) {
      inner = this._renderAgentDetail()
    } else {
      inner = this._renderHome()
    }

    this.innerHTML = `
      <div class="agents-app">
        ${this._note ? `<div class="agents-note" data-clear-note>${escapeHTML(this._note)}</div>` : ''}
        ${inner}
      </div>`

    this.querySelector<HTMLElement>('[data-clear-note]')?.addEventListener('click', () => {
      this._note = null
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-home]').forEach((el) => el.addEventListener('click', () => this._goHome()))

    if (this._shared === 'models') this._wireModels()
    else if (this._shared === 'connections') this._wireConnections()
    else if (this._shared === 'inbox') this._wireInbox()
    else if (this._selected) this._wireAgentDetail()
    else this._wireHome()
  }

  private _renderBack(): string {
    return `<button class="agents-back" data-home>← Agents</button>`
  }

  // ---- home: agent card grid -----------------------------------------------

  private _renderHome(): string {
    const pending = this._inbox.filter((i) => i.state === 'pending').length
    const count = (agent: string, arr: { spec: { agentRef: string } }[]) => arr.filter((x) => x.spec.agentRef === agent).length
    const cards = this._agents
      .map((a) => {
        const model = a.spec?.models?.chat
        const nsched = count(a.metadata.name, this._schedules)
        const ntrig = count(a.metadata.name, this._triggers)
        const chan = a.spec?.defaultNotifyConnection
        return `
          <article class="agents-card" data-agent="${escapeHTML(a.metadata.name)}">
            <div class="agents-card-glyph">🤖</div>
            <div class="agents-card-body">
              <h3>${escapeHTML(a.spec?.displayName || a.metadata.name)}</h3>
              <p class="agents-card-model ${model ? '' : 'warn'}">${model ? escapeHTML(model) : 'no model — set up in Settings'}</p>
            </div>
            <div class="agents-card-foot">
              <span>${nsched} schedule${nsched === 1 ? '' : 's'}</span>
              <span>${ntrig} trigger${ntrig === 1 ? '' : 's'}</span>
              <span>${chan ? '📣 ' + escapeHTML(chan) : 'no channel'}</span>
            </div>
          </article>`
      })
      .join('')

    return `
      <div class="agents-home">
        <header class="agents-home-head">
          <h1>Agents</h1>
          <div class="agents-home-actions">
            <button class="secondary" data-shared="models">⚙ Models${this._credentials.length ? ` · ${this._credentials.length}` : ''}</button>
            <button class="secondary" data-shared="connections">🔌 Connections${this._connections.length ? ` · ${this._connections.length}` : ''}</button>
            <button class="secondary" data-shared="inbox">📥 Inbox${pending ? ` · ${pending}` : ''}</button>
          </div>
        </header>
        ${this._error ? `<div class="agents-err">${escapeHTML(this._error)}</div>` : ''}
        <div class="agents-grid">
          <form class="agents-card agents-card-new">
            <div class="agents-card-glyph">＋</div>
            <input name="name" placeholder="new-agent-id" required pattern="[a-z0-9-]+" />
            <button>Create agent</button>
          </form>
          ${cards}
        </div>
      </div>`
  }
  private _wireHome(): void {
    this.querySelectorAll<HTMLElement>('.agents-card[data-agent]').forEach((el) => el.addEventListener('click', () => this._selectAgent(el.dataset.agent!)))
    this.querySelectorAll<HTMLElement>('[data-shared]').forEach((el) => el.addEventListener('click', () => this._openShared(el.dataset.shared as SharedView)))
    const nf = this.querySelector<HTMLFormElement>('.agents-card-new')
    nf?.addEventListener('submit', (e) => {
      e.preventDefault()
      const v = nf.querySelector<HTMLInputElement>('input')!.value.trim()
      if (v) void this._createAgent(v, '')
    })
  }

  // ---- agent detail --------------------------------------------------------

  private _renderAgentDetail(): string {
    const a = this._agent()
    if (!a) return `<div class="agents-empty"><p class="muted">Agent not found.</p></div>`
    const flow = this._agentView === 'flow'
    const listBody = flow
      ? ''
      : this._agentTab === 'chat'
        ? this._renderChat(a)
        : this._agentTab === 'schedules'
          ? this._renderSchedules(a)
          : this._agentTab === 'triggers'
            ? this._renderTriggers(a)
            : this._agentTab === 'channels'
              ? this._renderChannels(a)
              : this._renderSettings(a)
    return `
      <div class="agents-detail ${flow ? 'is-flow' : ''}">
        <div class="agents-detail-head">
          <div class="agents-detail-title">
            ${this._renderBack()}
            <h2>${escapeHTML(a.spec?.displayName || a.metadata.name)}</h2>
          </div>
          <div class="agents-detail-actions">
            <div class="agents-viewseg">
              <button class="${flow ? 'on' : ''}" data-view="flow">◆ Flow</button>
              <button class="${flow ? '' : 'on'}" data-view="list">☰ List</button>
            </div>
            <button class="secondary" data-delagent="${escapeHTML(a.metadata.name)}">Delete agent</button>
          </div>
        </div>
        ${
          flow
            ? `<div class="agents-flow-host" data-flow-host></div>`
            : `<nav class="agents-subnav">
                 ${AGENT_TABS.map(([id, label]) => `<button class="agents-subtab ${this._agentTab === id ? 'sel' : ''}" data-subtab="${id}">${label}</button>`).join('')}
               </nav>
               <div class="agents-detail-body">${listBody}</div>`
        }
      </div>`
  }
  private _wireAgentDetail(): void {
    this.querySelectorAll<HTMLElement>('[data-view]').forEach((el) =>
      el.addEventListener('click', () => {
        const v = el.dataset.view as 'flow' | 'list'
        if (v === this._agentView) return
        this._agentView = v
        this._render()
      }),
    )
    this.querySelector<HTMLElement>('[data-delagent]')?.addEventListener('click', (e) => {
      const name = (e.currentTarget as HTMLElement).dataset.delagent!
      if (confirm(`Delete agent ${name} and its history?`)) void this._deleteAgent(name)
    })
    if (this._agentView === 'flow') {
      this._mountFlow()
      return
    }
    this.querySelectorAll<HTMLElement>('[data-subtab]').forEach((el) =>
      el.addEventListener('click', () => {
        this._agentTab = el.dataset.subtab as AgentTab
        this._render()
      }),
    )
    if (this._agentTab === 'chat') this._wireChat()
    else if (this._agentTab === 'schedules') this._wireSchedules()
    else if (this._agentTab === 'triggers') this._wireTriggers()
    else if (this._agentTab === 'channels') this._wireChannels()
    else this._wireSettings()
  }

  // ---- flow canvas ----------------------------------------------------------

  // Mount (or re-attach) the imperative FlowCanvas into the freshly-rendered
  // host. The portal replaces innerHTML on every _render(), so the canvas DOM is
  // kept alive off-tree via _flowRoot and re-appended here — that preserves its
  // pan/zoom/positions/selection across re-renders. The instance is reused
  // across agents; update() re-keys on the agent name.
  private _mountFlow(): void {
    const host = this.querySelector<HTMLElement>('[data-flow-host]')
    if (!host) return
    if (!this._flow || !this._flowRoot) {
      this._flowRoot = document.createElement('div')
      this._flow = new FlowCanvas(this._flowRoot, this._flowCallbacks())
    }
    host.appendChild(this._flowRoot)
    this._flow.update(this._flowModel())
  }

  private _flowCallbacks(): FlowCallbacks {
    return {
      onEdit: (id, values) => void this._flowEdit(id, values),
      onLink: (from, to) => void this._flowLink(from, to),
      onAdd: (t) => this._flowAdd(t),
      onRun: (id) => void this._flowRun(id),
      onDelete: (id) => void this._flowDelete(id),
      onOpenChat: () => {
        this._agentView = 'list'
        this._agentTab = 'chat'
        this._render()
      },
      onToast: (m) => this._flow?.toast(m),
      draftFor: (t) => this._flowDraftFor(t),
      create: (t, values) => this._flowCreate(t, values),
    }
  }

  // ---- flow: drag-to-create -------------------------------------------------

  // The create-form spec for a draggable node type (null = not a standalone
  // object). Schedule/trigger/model wire straight into the agent; a connection
  // stands alone until you wire it.
  private _flowDraftFor(key: string): DraftSpec | null {
    const nameField = { key: 'name', label: 'Name', kind: 'text' as const, placeholder: 'lowercase-with-dashes', hint: 'a-z, 0-9 and dashes' }
    if (key === 'schedule')
      return {
        title: 'new schedule',
        nodeType: 'schedule',
        ins: [],
        outs: ['fire'],
        outPort: 'fire',
        agentPort: 'input',
        fields: [
          nameField,
          { key: 'schedule', label: 'Cron', kind: 'text', mono: true, value: '0 9 * * *', placeholder: '0 9 * * *', hint: '5-field cron · crontab.guru' },
          { key: 'timeZone', label: 'Timezone', kind: 'text', placeholder: 'Europe/Vilnius' },
          { key: 'task', label: 'Task', kind: 'textarea', placeholder: 'Summarise today’s open PRs and post to my channel.' },
        ],
      }
    if (key === 'trigger')
      return {
        title: 'new trigger',
        nodeType: 'trigger',
        ins: ['src'],
        outs: ['fire'],
        outPort: 'fire',
        agentPort: 'input',
        fields: [
          nameField,
          { key: 'source', label: 'Source', kind: 'select', value: 'webhook', options: ['webhook', 'github', 'channel'].map((v) => ({ value: v, label: v })) },
          { key: 'connectionRef', label: 'Connection', kind: 'select', value: '', options: [{ value: '', label: '— none —' }, ...this._connections.map((c) => ({ value: c.metadata.name, label: c.metadata.name }))] },
          { key: 'task', label: 'Task on fire', kind: 'textarea', placeholder: 'Triage the incoming event.' },
        ],
      }
    if (key === 'model')
      return {
        title: 'new model',
        nodeType: 'model',
        ins: [],
        outs: ['infer'],
        outPort: 'infer',
        agentPort: 'model',
        fields: [
          nameField,
          { key: 'provider', label: 'Provider', kind: 'select', value: 'openai', options: ['openai', 'anthropic', 'custom'].map((v) => ({ value: v, label: v })) },
          { key: 'baseURL', label: 'Base URL', kind: 'text', mono: true, placeholder: 'https://api.openai.com/v1' },
          { key: 'model', label: 'Model', kind: 'text', mono: true, placeholder: 'gpt-4o' },
          { key: 'apiKey', label: 'API key', kind: 'text', mono: true, placeholder: 'sk-…', hint: 'stored as a Secret' },
        ],
      }
    if (key === 'connection')
      return {
        title: 'new connection',
        nodeType: 'connection',
        ins: ['notify'],
        outs: ['events'],
        fields: [
          nameField,
          { key: 'type', label: 'Type', kind: 'select', value: 'discord', options: Object.keys(CONN_CATEGORY).map((v) => ({ value: v, label: v })) },
          { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'optional' },
        ],
      }
    // Tool nodes: tool-type Connections. Wire one into a Toolset to enable it.
    if (key === 'tool-mcp')
      return {
        title: 'new mcp tool',
        nodeType: 'tool',
        ins: [],
        outs: ['events'],
        fields: [
          nameField,
          { key: 'baseURL', label: 'MCP server URL', kind: 'text', mono: true, placeholder: 'https://host/mcp', hint: 'the remote MCP endpoint' },
          { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'optional' },
        ],
      }
    if (key === 'tool-github')
      return {
        title: 'new github tool',
        nodeType: 'tool',
        ins: [],
        outs: ['events'],
        fields: [nameField, { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'optional' }],
      }
    if (key === 'tool-web')
      return {
        title: 'web search',
        nodeType: 'tool',
        ins: [],
        outs: ['events'],
        fields: [nameField, { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'optional' }],
      }
    if (key === 'toolset')
      return {
        title: 'new toolset',
        nodeType: 'toolset',
        ins: ['tool'],
        outs: ['use'],
        outPort: 'use',
        agentPort: 'tools',
        fields: [
          nameField,
          { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'e.g. dev-tools' },
          {
            key: 'families',
            label: 'Built-in families',
            kind: 'chips',
            chips: ['web', 'github', 'mcp', 'edges'].map((f) => ({ value: f, label: f, on: false })),
            hint: 'A shared bundle. Wire tools into it after creating.',
          },
        ],
      }
    return null // chat / tools (built-in) / notify / delegate are not standalone objects
  }

  // Write the object a draft describes; return its real flow-node id on success.
  private async _flowCreate(key: string, values: Record<string, string | string[]>): Promise<string | null> {
    const s = (k: string): string => String(values[k] ?? '').trim()
    const name = s('name')
    if (!/^[a-z0-9-]+$/.test(name)) {
      this._flow?.toast('Name must be lowercase letters, numbers and dashes')
      return null
    }
    const agent = this._selected as string
    try {
      if (key === 'schedule') {
        await this._send('POST', '/api/schedules', { name, agentRef: agent, type: 'cron', schedule: s('schedule'), timeZone: s('timeZone'), task: s('task') })
        await this._loadSchedules()
        return 'sched:' + name
      }
      if (key === 'trigger') {
        await this._send('POST', '/api/triggers', { name, agentRef: agent, source: s('source') || 'webhook', connectionRef: s('connectionRef'), task: s('task') })
        await this._loadTriggers()
        return 'trig:' + name
      }
      if (key === 'model') {
        await this._send('POST', '/api/credentials', { name, provider: s('provider'), baseURL: s('baseURL'), model: s('model'), apiKey: s('apiKey') })
        await this._send('PUT', `/api/agents/${encodeURIComponent(agent)}`, { modelCredential: name })
        await this._loadCredentials()
        await this._loadAgents()
        return 'model:' + name
      }
      if (key === 'connection') {
        await this._send('POST', '/api/connections', { name, type: s('type'), displayName: s('displayName') })
        await this._loadConnections()
        return 'conn:' + name
      }
      // Tools are tool-type Connections.
      const toolType: Record<string, string> = { 'tool-mcp': 'mcp', 'tool-github': 'github', 'tool-web': 'websearch' }
      if (key in toolType) {
        await this._send('POST', '/api/connections', { name, type: toolType[key], baseURL: s('baseURL'), displayName: s('displayName') })
        await this._loadConnections()
        return 'conn:' + name
      }
      if (key === 'toolset') {
        const families = ['core', ...((values.families as string[]) || [])]
        await this._send('POST', '/api/toolsets', { name, displayName: s('displayName'), families })
        // link the new toolset to this agent's interactive tools
        await this._linkToolset(agent, name)
        await this._loadToolsets()
        await this._loadAgents()
        return 'toolset:' + name
      }
    } catch (e) {
      this._flow?.toast('Create failed: ' + (e as Error).message)
      return null
    }
    return null
  }

  // _linkToolset adds a toolset name to an agent's interactive tool policy
  // (idempotent), preserving the rest of the list.
  private async _linkToolset(agent: string, toolset: string): Promise<void> {
    const a = this._agents.find((x) => x.metadata.name === agent)
    const cur = a?.spec?.tools?.interactive?.toolsets || []
    if (cur.includes(toolset)) return
    await this._send('PUT', `/api/agents/${encodeURIComponent(agent)}`, { interactiveToolsets: [...cur, toolset] })
  }

  private _flowModel(): FlowModel {
    const a = this._agent()!
    const name = a.metadata.name
    const nodes: FNode[] = []
    const wires: FWire[] = []
    const scheds = this._schedules.filter((s) => s.spec.agentRef === name)
    const trigs = this._triggers.filter((t) => t.spec.agentRef === name)
    const model = a.spec?.models?.chat
    const notify = a.spec?.defaultNotifyConnection
    const usedConns = new Set<string>()
    trigs.forEach((t) => t.spec.connectionRef && usedConns.add(t.spec.connectionRef))
    if (notify) usedConns.add(notify)

    // agent core
    nodes.push({
      id: 'agent',
      type: 'agent',
      title: a.spec?.displayName || name,
      core: true,
      ins: ['input', 'model', 'tools'],
      outs: ['result', 'delegate'],
      sub: a.spec?.description ? escapeHTML(a.spec.description) : escapeHTML((a.spec?.systemPrompt || 'No system prompt yet.').slice(0, 96)),
      status: ['ok', 'autonomy: ' + (a.spec?.autonomy || 'ask')],
      fields: [
        { key: 'displayName', label: 'Display name', kind: 'text', value: a.spec?.displayName || name },
        { key: 'systemPrompt', label: 'System prompt', kind: 'textarea', value: a.spec?.systemPrompt || '', placeholder: 'You are a concise assistant that…' },
        { key: 'autonomy', label: 'Autonomy', kind: 'select', value: a.spec?.autonomy || 'ask', options: ['suggest', 'ask', 'auto'].map((v) => ({ value: v, label: v })) },
        {
          key: 'families',
          label: 'Built-in tools',
          kind: 'chips',
          chips: ['web', 'github', 'mcp', 'edges'].map((f) => ({ value: f, label: f, on: new Set(a.spec?.tools?.interactive?.families || ['core', 'web', 'github', 'mcp', 'edges']).has(f) })),
          hint: 'Capabilities this agent has directly. Link a Toolset for shared bundles.',
        },
      ],
    })

    // chat
    nodes.push({ id: 'chat', type: 'chat', title: 'interactive', ins: [], outs: ['msg'], sub: 'live console — talk to it now', status: [model ? 'ok' : 'warn', model ? 'ready' : 'no model'] })
    wires.push({ from: ['chat', 'msg'], to: ['agent', 'input'] })

    // schedules
    for (const s of scheds) {
      const id = 'sched:' + s.metadata.name
      const when = s.spec.type === 'wakeup' ? s.spec.runAt || '' : s.spec.schedule || ''
      const dis = s.status?.disabledReason
      nodes.push({
        id,
        type: 'schedule',
        title: s.metadata.name,
        ins: [],
        outs: ['fire'],
        sub: `<span class="mono">${escapeHTML(when)}</span>${s.spec.timeZone ? ' · ' + escapeHTML(s.spec.timeZone) : ''}`,
        status: dis ? ['off', dis] : s.spec.suspend ? ['off', 'paused'] : ['ok', s.status?.nextRun ? 'next ' + this._fmtTime(s.status.nextRun) : 'armed'],
        canRun: true,
        canDelete: true,
        fields: [
          { key: 'schedule', label: 'Cron', kind: 'text', mono: true, value: s.spec.schedule || '', placeholder: '0 9 * * *', hint: '5-field cron · crontab.guru' },
          { key: 'timeZone', label: 'Timezone', kind: 'text', value: s.spec.timeZone || '' },
          { key: 'task', label: 'Task', kind: 'textarea', value: s.spec.task || s.spec.checklist || '' },
        ],
      })
      wires.push({ from: [id, 'fire'], to: ['agent', 'input'] })
    }

    // triggers
    for (const t of trigs) {
      const id = 'trig:' + t.metadata.name
      nodes.push({
        id,
        type: 'trigger',
        title: t.metadata.name,
        ins: ['src'],
        outs: ['fire'],
        sub: `source <span class="mono">${escapeHTML(t.spec.source)}</span>${t.spec.connectionRef ? ' · ' + escapeHTML(t.spec.connectionRef) : ''}`,
        status: t.spec.suspend ? ['off', 'paused'] : ['ok', t.status?.lastFired ? 'last ' + this._fmtTime(t.status.lastFired) : 'armed'],
        canRun: true,
        canDelete: true,
        fields: [
          { key: 'source', label: 'Source', kind: 'select', value: t.spec.source, options: ['webhook', 'github', 'channel'].map((v) => ({ value: v, label: v })) },
          {
            key: 'connectionRef',
            label: 'Connection',
            kind: 'select',
            value: t.spec.connectionRef || '',
            options: [{ value: '', label: '— none —' }, ...this._connections.map((c) => ({ value: c.metadata.name, label: c.metadata.name }))],
          },
          { key: 'task', label: 'Task on fire', kind: 'textarea', value: t.spec.task || '' },
        ],
      })
      wires.push({ from: [id, 'fire'], to: ['agent', 'input'] })
    }

    // model (active chat credential)
    if (model) {
      const id = 'model:' + model
      const c = this._credentials.find((x) => x.name === model)
      nodes.push({
        id,
        type: 'model',
        title: model,
        ins: [],
        outs: ['infer'],
        sub: `<span class="mono">${escapeHTML(c?.provider || 'model')}</span>${c?.model ? ' · ' + escapeHTML(c.model) : ''}`,
        status: c?.hasAPIKey ? ['ok', 'key set'] : ['warn', 'no key'],
        fields: [
          {
            key: 'modelCredential',
            label: 'Credential',
            kind: 'select',
            value: model,
            options: [{ value: '', label: '— no model —' }, ...this._credentials.map((x) => ({ value: x.name, label: x.name + (x.model ? ` (${x.model})` : '') }))],
            hint: 'Switch which credential this agent reasons with.',
          },
        ],
      })
      wires.push({ from: [id, 'infer'], to: ['agent', 'model'] })
    }

    // The agent's own built-in families live on the Agent node's settings
    // (see fields above) — not a separate bundle node — so "Tool" and "Toolset"
    // stay the only two tool concepts on the canvas.
    const known = ['core', 'web', 'github', 'mcp', 'edges']

    // toolsets (shared bundles; all render, linked ones wire into agent.tools)
    const linked = new Set([...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])])
    for (const ts of this._toolsets) {
      const tn = ts.metadata.name
      const id = 'toolset:' + tn
      const tfams = ts.spec.families || []
      const tconns = ts.spec.connections || []
      nodes.push({
        id,
        type: 'toolset',
        title: ts.spec.displayName || tn,
        ins: ['tool'],
        outs: ['use'],
        tags: tfams.length ? tfams.filter((f) => f !== 'core') : undefined,
        sub: ts.spec.description ? escapeHTML(ts.spec.description) : `<span class="mono">shared</span>${tconns.length ? ` · ${tconns.length} tool${tconns.length === 1 ? '' : 's'}` : ''}`,
        status: linked.has(tn) ? ['ok', 'linked'] : ['off', 'available'],
        canDelete: true,
        fields: [
          { key: 'displayName', label: 'Display name', kind: 'text', value: ts.spec.displayName || tn },
          { key: 'families', label: 'Built-in families', kind: 'chips', chips: known.filter((f) => f !== 'core').map((f) => ({ value: f, label: f, on: tfams.includes(f) })) },
          { key: 'connections', label: 'Tools', kind: 'static', value: tconns.join(', ') || '— drag a tool here —' },
        ],
      })
      if (linked.has(tn)) wires.push({ from: [id, 'use'], to: ['agent', 'tools'] })
      for (const cn of tconns) if (this._connections.some((c) => c.metadata.name === cn)) wires.push({ from: ['conn:' + cn, 'events'], to: [id, 'tool'] })
    }

    // connections: tool-category ones (mcp/github/websearch) render as Tool
    // nodes; channel ones as Connection nodes (notify sink + event source).
    for (const c of this._connections) {
      const cn = c.metadata.name
      const cat = CONN_CATEGORY[c.spec.type]
      const isChannel = cat === 'channel'
      const isTool = cat === 'tool'
      const id = 'conn:' + cn
      const used = usedConns.has(cn)
      const webish = c.spec.type === 'websearch'
      nodes.push({
        id,
        type: isTool ? 'tool' : 'connection',
        title: c.spec.displayName || cn,
        ins: isChannel ? ['notify'] : [],
        outs: ['events'],
        sub: `<span class="mono">${escapeHTML(c.spec.type)}</span>${c.status?.oauthConnected ? ' · connected' : webish ? '' : used ? '' : ' · unwired'}`,
        status: c.status?.oauthConnected || c.status?.phase === 'Ready' || webish ? ['ok', webish ? 'ready' : 'connected'] : ['warn', c.status?.phase || 'setup'],
        canDelete: isTool,
        fields: [
          { key: 'type', label: 'Type', kind: 'static', value: c.spec.type },
          ...(c.spec.baseURL ? [{ key: 'baseURL', label: 'Endpoint', kind: 'static' as const, mono: true, value: c.spec.baseURL }] : []),
          { key: 'phase', label: 'Status', kind: 'static', value: c.status?.phase || (webish ? 'ready' : c.status?.oauthConnected ? 'connected' : 'setup') },
        ],
      })
      trigs.forEach((t) => {
        if (t.spec.connectionRef === cn) wires.push({ from: [id, 'events'], to: ['trig:' + t.metadata.name, 'src'] })
      })
      if (notify === cn && isChannel) wires.push({ from: ['agent', 'result'], to: [id, 'notify'] })
    }

    // delegates
    for (const d of a.spec?.delegates || []) {
      const id = 'delegate:' + d
      nodes.push({ id, type: 'delegate', title: d, ins: ['call'], outs: [], sub: 'sub-agent', status: ['off', 'on demand'], canDelete: true })
      wires.push({ from: ['agent', 'delegate'], to: [id, 'call'] })
    }

    return { key: name, nodes, wires }
  }

  private _fmtTime(iso: string): string {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    const diff = d.getTime() - Date.now()
    const abs = Math.abs(diff)
    const m = Math.round(abs / 60000)
    if (m < 60) return diff > 0 ? `in ${m}m` : `${m}m ago`
    const h = Math.round(m / 60)
    if (h < 48) return diff > 0 ? `in ${h}h` : `${h}h ago`
    return d.toLocaleDateString()
  }

  private async _flowEdit(id: string, values: Record<string, string | string[]>): Promise<void> {
    const str = (v: string | string[]): string => (Array.isArray(v) ? v.join(',') : v)
    // Build a patch from only the keys actually present, so a single-field
    // (auto-)save never blanks the fields it didn't include.
    const patch: Record<string, unknown> = {}
    const take = (k: string, as = k): void => {
      if (k in values) patch[as] = str(values[k])
    }
    if (id === 'agent') {
      take('displayName')
      take('systemPrompt')
      take('autonomy')
      if ('families' in values) {
        const fams = ['core', ...((values.families as string[]) || [])]
        patch.interactiveFamilies = fams
        patch.backgroundFamilies = fams.filter((f) => f === 'core' || f === 'web')
      }
      if (Object.keys(patch).length) await this._updateAgent(patch)
    } else if (id.startsWith('sched:')) {
      take('schedule')
      take('timeZone')
      take('task')
      if (Object.keys(patch).length) await this._updateSchedule(id.slice(6), patch)
    } else if (id.startsWith('trig:')) {
      take('source')
      take('connectionRef')
      take('task')
      if (Object.keys(patch).length) await this._updateTrigger(id.slice(5), patch)
    } else if (id.startsWith('model:')) {
      if ('modelCredential' in values) await this._updateAgent({ modelCredential: str(values.modelCredential) }, 'Model reassigned.')
    } else if (id.startsWith('toolset:')) {
      const tp: Record<string, unknown> = {}
      if ('displayName' in values) tp.displayName = str(values.displayName)
      if ('families' in values) tp.families = ['core', ...((values.families as string[]) || [])]
      if (Object.keys(tp).length) await this._updateToolset(id.slice(8), tp)
    }
  }

  private async _updateToolset(name: string, patch: Record<string, unknown>): Promise<void> {
    try {
      await this._send('PUT', `/api/toolsets/${encodeURIComponent(name)}`, patch)
      this._note = 'Toolset updated.'
      await this._loadToolsets()
    } catch (e) {
      this._note = 'Update failed: ' + (e as Error).message
      this._render()
    }
  }

  // Interpret a dragged cable (out-port → in-port) as a real spec mutation.
  private async _flowLink(from: [string, string], to: [string, string]): Promise<void> {
    const [fromNode] = from
    const [toNode, toPort] = to
    // connection.events → trigger.src : point the trigger at this connection
    if (fromNode.startsWith('conn:') && toNode.startsWith('trig:') && toPort === 'src') {
      return void this._updateTrigger(toNode.slice(5), { connectionRef: fromNode.slice(5) }, 'Trigger connected.')
    }
    // model.infer → agent.model : switch the agent's model credential
    if (fromNode.startsWith('model:') && toNode === 'agent' && toPort === 'model') {
      return void this._updateAgent({ modelCredential: fromNode.slice(6) }, 'Model reassigned.')
    }
    // agent.result → connection.notify : set the default notify channel
    if (fromNode === 'agent' && toNode.startsWith('conn:') && toPort === 'notify') {
      return void this._updateAgent({ notifyConnection: toNode.slice(5) }, 'Notify channel set.')
    }
    // toolset.use → agent.tools : link the shared toolset to this agent
    if (fromNode.startsWith('toolset:') && toNode === 'agent' && toPort === 'tools') {
      await this._linkToolset(this._selected as string, fromNode.slice(8))
      this._note = 'Toolset linked.'
      await this._loadAgents()
      return
    }
    // tool.provide → toolset.tool : add the tool (a tool-type connection) to the
    // bundle, and enable the matching built-in family so it actually resolves.
    if (fromNode.startsWith('conn:') && toNode.startsWith('toolset:') && toPort === 'tool') {
      const tsName = toNode.slice(8)
      const cn = fromNode.slice(5)
      const conn = this._connections.find((c) => c.metadata.name === cn)
      const ts = this._toolsets.find((x) => x.metadata.name === tsName)
      const famFor: Record<string, string> = { mcp: 'mcp', github: 'github', websearch: 'web' }
      const fam = conn ? famFor[conn.spec.type] : undefined
      const conns = ts?.spec.connections || []
      const fams = ts?.spec.families || []
      const patch: Record<string, unknown> = {}
      // websearch has no server to dial → enable the 'web' family only.
      if (conn?.spec.type !== 'websearch' && !conns.includes(cn)) patch.connections = [...conns, cn]
      if (fam && !fams.includes(fam)) patch.families = [...fams, fam]
      if (Object.keys(patch).length) return void this._updateToolset(tsName, patch)
      return
    }
    this._flow?.toast('These ports don’t connect — try tool → toolset, toolset → agent, or schedule/trigger → agent.')
  }

  private _flowAdd(key: string): void {
    // Non-creatable palette items route to the matching form.
    if (key === 'chat') {
      this._agentView = 'list'
      this._agentTab = 'chat'
      this._render()
    } else if (key === 'output') {
      this._openShared('connections')
    } else if (key === 'delegate') {
      this._agentView = 'list'
      this._agentTab = 'settings'
      this._render()
    }
  }

  private async _flowRun(id: string): Promise<void> {
    if (id.startsWith('sched:')) await this._runSchedule(id.slice(6))
    else if (id.startsWith('trig:')) await this._runTrigger(id.slice(5))
  }

  private async _flowDelete(id: string): Promise<void> {
    if (id.startsWith('sched:')) {
      if (confirm(`Delete schedule ${id.slice(6)}?`)) await this._deleteSchedule(id.slice(6))
    } else if (id.startsWith('trig:')) {
      if (confirm(`Delete trigger ${id.slice(5)}?`)) await this._deleteTrigger(id.slice(5))
    } else if (id.startsWith('delegate:')) {
      const a = this._agent()
      const next = (a?.spec?.delegates || []).filter((d) => d !== id.slice(9))
      await this._updateAgent({ delegates: next }, 'Delegate removed.')
    } else if (id.startsWith('toolset:')) {
      // Unlink the shared toolset from this agent; the toolset object stays for
      // other agents. (Delete the object itself from the Toolsets list.)
      const ts = id.slice(8)
      const a = this._agent()
      const inter = (a?.spec?.tools?.interactive?.toolsets || []).filter((t) => t !== ts)
      const bg = (a?.spec?.tools?.background?.toolsets || []).filter((t) => t !== ts)
      await this._updateAgent({ interactiveToolsets: inter, backgroundToolsets: bg }, 'Toolset unlinked.')
    }
  }

  // ---- agent: chat ---------------------------------------------------------

  private _renderChat(a: Agent): string {
    if (!a.spec?.models?.chat) {
      return `<div class="agents-empty"><p class="muted">No model assigned. Open <strong>Settings</strong> and pick a model credential to start chatting.</p></div>`
    }
    return `
      <div class="agents-chat">
        <div class="agents-log">
          ${
            this._messages.length
              ? this._messages
                  .map((m) =>
                    m.role === 'tool'
                      ? `<div class="agents-msg tool ${m.error ? 'err' : ''}"><div class="agents-toolrow">🔧 ${escapeHTML(m.content)}</div></div>`
                      : `<div class="agents-msg ${m.role}"><div class="agents-role">${m.role}</div><div class="agents-body">${escapeHTML(m.content) || (this._streaming && m.role === 'assistant' ? '…' : '')}</div></div>`,
                  )
                  .join('')
              : `<p class="muted">No messages yet. Say hi.</p>`
          }
        </div>
        <form class="agents-chat-form"><input placeholder="Message ${escapeHTML(a.metadata.name)}…" ${this._streaming ? 'disabled' : ''} autocomplete="off" /><button ${this._streaming ? 'disabled' : ''}>${this._streaming ? '…' : 'Send'}</button></form>
      </div>`
  }
  private _wireChat(): void {
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

  // ---- agent: schedules ----------------------------------------------------

  private _renderSchedules(a: Agent): string {
    const mine = this._schedules.filter((s) => s.spec.agentRef === a.metadata.name)
    const editing = this._schedEdit ? mine.find((s) => s.metadata.name === this._schedEdit) : undefined
    const notifyConn = a.spec?.defaultNotifyConnection || ''
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
    const typeLabels: Record<string, string> = { cron: 'Repeating', heartbeat: 'Heartbeat', wakeup: 'One-time' }
    const channelBanner = notifyConn
      ? `<div class="agents-info">🔔 Output is delivered to <strong>${escapeHTML(notifyConn)}</strong>. Change it on the <strong>Channels</strong> tab.</div>`
      : `<div class="agents-warn-banner">🔕 No notify channel set — scheduled output won't reach you. Set one on the <strong>Channels</strong> tab.</div>`
    const type = this._schedType
    const freq = this._schedFreq
    const typeHelp: Record<string, string> = {
      cron: 'Runs your task on a repeating schedule and delivers the result to your channel.',
      heartbeat: 'Reviews a standing checklist on a repeating pulse — only pings you when something needs attention (quiet otherwise).',
      wakeup: 'Runs once at a specific date & time, then it’s done.',
    }
    const typeSelect = `<label>What kind?<select name="type" data-schedtype>${Object.entries(typeLabels)
      .map(([v, l]) => `<option value="${v}" ${v === type ? 'selected' : ''}>${l}</option>`)
      .join('')}</select></label>`
    const freqSelect = `<label>How often?<select name="freq" data-schedfreq>${[
      ['hourly', 'Every hour'],
      ['daily', 'Every day'],
      ['weekly', 'Every week'],
      ['custom', 'Custom (cron)'],
    ]
      .map(([v, l]) => `<option value="${v}" ${v === freq ? 'selected' : ''}>${l}</option>`)
      .join('')}</select></label>`
    const timeField = `<label>At what time?<input type="time" name="time" value="09:00" /></label>`
    const dowField = `<label>On which day?<select name="dow"><option value="1">Monday</option><option value="2">Tuesday</option><option value="3">Wednesday</option><option value="4">Thursday</option><option value="5">Friday</option><option value="6">Saturday</option><option value="0">Sunday</option></select></label>`
    let scheduleFields = ''
    if (type === 'wakeup') {
      scheduleFields = `<label>Run at<input type="datetime-local" name="runAtLocal" /></label>`
    } else {
      scheduleFields = freqSelect
      if (freq === 'daily') scheduleFields += timeField
      else if (freq === 'weekly') scheduleFields += dowField + timeField
      else if (freq === 'custom')
        scheduleFields += `<label>Cron expression<input name="schedule" placeholder="0 8 * * *" /><span class="agents-hint">5-field cron. <a href="https://crontab.guru" target="_blank" rel="noopener">crontab.guru</a> helps.</span></label>`
    }
    const taskLabel = type === 'heartbeat' ? 'Checklist' : 'Task'
    const taskPlaceholder =
      type === 'heartbeat' ? 'Check for failing CI and unanswered mentions; only ping me if something needs action.' : 'Summarize today’s open PRs and post the summary to my channel.'
    return `
      <div class="agents-panel">
        <p class="muted">Have ${escapeHTML(a.metadata.name)} run on its own on a timer. ▶ <strong>Run now</strong> executes a schedule immediately as a test (and delivers to your channel), so you don’t have to wait for the timer.</p>
        ${channelBanner}
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Kind</th><th>When</th><th>Next run</th><th class="agents-th-actions">Actions</th></tr></thead>
          <tbody>
            ${
              mine.length
                ? mine
                    .map(
                      (s) => `<tr>
                        <td><span class="agents-cell-name">${escapeHTML(s.metadata.name)}</span>${s.spec.suspend ? '<span class="agents-badge agents-badge-muted">suspended</span>' : ''}${s.status?.disabledReason ? ` <span class="agents-warn-dot" title="${escapeHTML(s.status.disabledReason)}">●</span>` : ''}</td>
                        <td><span class="agents-badge">${escapeHTML(typeLabels[s.spec.type] || s.spec.type)}</span></td>
                        <td>${s.spec.type === 'wakeup' ? escapeHTML(s.spec.runAt || '—') : `<code>${escapeHTML(s.spec.schedule || '—')}</code>`}${s.spec.timeZone ? ` <span class="muted">${escapeHTML(s.spec.timeZone)}</span>` : ''}</td>
                        <td class="muted">${s.status?.nextRun ? escapeHTML(s.status.nextRun) : '—'}</td>
                        <td class="agents-row-actions">
                          <button class="agents-iconbtn" data-editsched="${escapeHTML(s.metadata.name)}" title="Edit">✏️</button>
                          <button class="agents-iconbtn" data-run="${escapeHTML(s.metadata.name)}" title="Run now (test)">▶</button>
                          <button class="agents-iconbtn" data-suspendsched="${escapeHTML(s.metadata.name)}" data-suspend="${s.spec.suspend ? '0' : '1'}" title="${s.spec.suspend ? 'Resume' : 'Pause'}">${s.spec.suspend ? '⏵' : '⏸'}</button>
                          <button class="agents-iconbtn agents-iconbtn-danger" data-delsched="${escapeHTML(s.metadata.name)}" title="Delete">🗑</button>
                        </td>
                      </tr>`,
                    )
                    .join('')
                : `<tr class="agents-empty-row"><td colspan="5"><span class="agents-empty">⏰ No schedules yet — create one below.</span></td></tr>`
            }
          </tbody>
        </table>
        ${editing ? this._renderScheduleEditForm(editing) : `<form class="agents-sched-form">
          <h4>New schedule</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="daily-digest" /></label>
            ${typeSelect}
          </div>
          <p class="agents-hint">${typeHelp[type]}</p>
          <div class="agents-grid2">
            ${scheduleFields}
          </div>
          ${type !== 'wakeup' && freq !== 'custom' ? `<p class="agents-hint">Times are in your local zone (${escapeHTML(tz)}).</p>` : ''}
          <label>${taskLabel}<textarea name="task" rows="2" placeholder="${escapeHTML(taskPlaceholder)}"></textarea></label>
          <button>Create schedule</button>
        </form>`}
      </div>`
  }
  private _renderScheduleEditForm(s: Schedule): string {
    const isWakeup = s.spec.type === 'wakeup'
    const isHeartbeat = s.spec.type === 'heartbeat'
    const taskLabel = isHeartbeat ? 'Checklist' : 'Task'
    const taskVal = isHeartbeat ? s.spec.checklist || '' : s.spec.task || ''
    // datetime-local wants "YYYY-MM-DDTHH:MM" in local time.
    const runAtLocal = s.spec.runAt ? new Date(s.spec.runAt).toISOString().slice(0, 16) : ''
    const when = isWakeup
      ? `<label>Run at<input type="datetime-local" name="runAtLocal" value="${escapeHTML(runAtLocal)}" /></label>`
      : `<label>Cron expression<input name="schedule" value="${escapeHTML(s.spec.schedule || '')}" /><span class="agents-hint">5-field cron. <a href="https://crontab.guru" target="_blank" rel="noopener">crontab.guru</a> helps.</span></label>
         <label>Time zone (IANA, optional)<input name="timeZone" value="${escapeHTML(s.spec.timeZone || '')}" placeholder="UTC" /></label>`
    return `<form class="agents-sched-form" data-edit="${escapeHTML(s.metadata.name)}" data-edittype="${escapeHTML(s.spec.type)}">
        <h4>Edit schedule <code>${escapeHTML(s.metadata.name)}</code></h4>
        <div class="agents-grid2">${when}</div>
        <label>${taskLabel}<textarea name="task" rows="3">${escapeHTML(taskVal)}</textarea></label>
        <label class="agents-check"><input type="checkbox" name="suspend" ${s.spec.suspend ? 'checked' : ''} /> Suspended (paused)</label>
        <div class="agents-form-actions"><button>Save changes</button><button type="button" class="secondary" data-schedcancel>Cancel</button></div>
      </form>`
  }
  private _wireSchedules(): void {
    this.querySelectorAll<HTMLElement>('[data-run]').forEach((el) => el.addEventListener('click', () => void this._runSchedule(el.dataset.run!)))
    this.querySelectorAll<HTMLElement>('[data-delsched]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete schedule ${el.dataset.delsched}?`)) void this._deleteSchedule(el.dataset.delsched!)
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-editsched]').forEach((el) =>
      el.addEventListener('click', () => {
        this._schedEdit = el.dataset.editsched!
        this._render()
      }),
    )
    this.querySelector<HTMLElement>('[data-schedcancel]')?.addEventListener('click', () => {
      this._schedEdit = null
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-suspendsched]').forEach((el) =>
      el.addEventListener('click', () => {
        const on = el.dataset.suspend === '1'
        void this._updateSchedule(el.dataset.suspendsched!, { suspend: on }, on ? 'Schedule paused.' : 'Schedule resumed.')
      }),
    )
    this.querySelector<HTMLSelectElement>('[data-schedtype]')?.addEventListener('change', (e) => {
      this._schedType = (e.target as HTMLSelectElement).value
      this._render()
    })
    this.querySelector<HTMLSelectElement>('[data-schedfreq]')?.addEventListener('change', (e) => {
      this._schedFreq = (e.target as HTMLSelectElement).value
      this._render()
    })
    const f = this.querySelector<HTMLFormElement>('.agents-sched-form')
    f?.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
      const editName = f.dataset.edit
      if (editName) {
        const editType = f.dataset.edittype
        const suspend = f.querySelector<HTMLInputElement>('[name=suspend]')?.checked || false
        const patch: Record<string, unknown> = { suspend }
        if (editType === 'wakeup') {
          const local = g('runAtLocal')
          if (local) patch.runAt = new Date(local).toISOString()
        } else {
          patch.schedule = g('schedule')
          patch.timeZone = g('timeZone')
        }
        if (editType === 'heartbeat') patch.checklist = g('task')
        else patch.task = g('task')
        void this._updateSchedule(editName, patch)
        return
      }
      const type = this._schedType
      const body: Record<string, unknown> = { name: g('name'), agentRef: this._selected, type }
      if (type === 'wakeup') {
        const local = g('runAtLocal')
        if (local) body.runAt = new Date(local).toISOString()
        body.task = g('task')
      } else {
        const freq = this._schedFreq
        if (freq === 'custom') {
          body.schedule = g('schedule')
        } else {
          const [hh, mm] = (g('time') || '09:00').split(':').map((n) => parseInt(n, 10))
          if (freq === 'hourly') body.schedule = '0 * * * *'
          else if (freq === 'weekly') body.schedule = `${mm} ${hh} * * ${g('dow') || '1'}`
          else body.schedule = `${mm} ${hh} * * *`
          body.timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
        }
        if (type === 'heartbeat') body.checklist = g('task')
        else body.task = g('task')
      }
      void this._createSchedule(body)
    })
  }

  // ---- agent: triggers -----------------------------------------------------

  private _renderTriggers(a: Agent): string {
    const mine = this._triggers.filter((t) => t.spec.agentRef === a.metadata.name)
    const editing = this._trigEdit ? mine.find((t) => t.metadata.name === this._trigEdit) : undefined
    const sourceOpts = (sel: string) =>
      ['webhook', 'github', 'channel', 'connection']
        .map((s) => `<option value="${s}" ${s === sel ? 'selected' : ''}>${s}</option>`)
        .join('')
    const connOptions = (sel: string) =>
      `<option value="">— none —</option>` +
      this._connections.map((c) => `<option value="${escapeHTML(c.metadata.name)}" ${c.metadata.name === sel ? 'selected' : ''}>${escapeHTML(c.metadata.name)} (${escapeHTML(c.spec.type)})</option>`).join('')
    const rows = mine
      .map(
        (t) => `<tr class="${this._trigEdit === t.metadata.name ? 'is-editing' : ''}">
                        <td><span class="agents-cell-name">${escapeHTML(t.metadata.name)}</span>${t.spec.suspend ? '<span class="agents-badge agents-badge-muted">suspended</span>' : ''}</td>
                        <td><span class="agents-badge">${escapeHTML(t.spec.source)}</span></td>
                        <td>${escapeHTML(t.spec.connectionRef || '—')}</td>
                        <td class="agents-hook-cell">${
                          t.status?.webhookPath
                            ? `<code class="agents-url" title="${escapeHTML(location.origin + t.status.webhookPath)}">${escapeHTML(midTrim(location.origin + t.status.webhookPath))}</code><button class="agents-iconbtn agents-iconbtn-sm" data-copyhook="${escapeHTML(t.status.webhookPath)}" title="Copy full webhook URL">📋</button>`
                            : '<span class="muted">—</span>'
                        }</td>
                        <td class="agents-cell-task muted">${escapeHTML((t.spec.task || '—').slice(0, 48))}${(t.spec.task || '').length > 48 ? '…' : ''}</td>
                        <td class="agents-row-actions">
                          <button class="agents-iconbtn" data-edittrig="${escapeHTML(t.metadata.name)}" title="Edit">✏️</button>
                          <button class="agents-iconbtn" data-firetrig="${escapeHTML(t.metadata.name)}" title="Fire now">▶</button>
                          <button class="agents-iconbtn" data-suspendtrig="${escapeHTML(t.metadata.name)}" data-suspend="${t.spec.suspend ? '0' : '1'}" title="${t.spec.suspend ? 'Resume' : 'Pause'}">${t.spec.suspend ? '⏵' : '⏸'}</button>
                          <button class="agents-iconbtn agents-iconbtn-danger" data-deltrig="${escapeHTML(t.metadata.name)}" title="Delete">🗑</button>
                        </td>
                      </tr>`,
      )
      .join('')
    const form = editing
      ? `<form class="agents-trig-form" data-edit="${escapeHTML(editing.metadata.name)}">
          <h4>Edit trigger <code>${escapeHTML(editing.metadata.name)}</code></h4>
          <div class="agents-grid2">
            <label>Source<select name="source">${sourceOpts(editing.spec.source)}</select></label>
            <label>Connection<select name="connectionRef">${connOptions(editing.spec.connectionRef || '')}</select></label>
          </div>
          <label>Task<textarea name="task" rows="3">${escapeHTML(editing.spec.task || '')}</textarea></label>
          <label class="agents-check"><input type="checkbox" name="suspend" ${editing.spec.suspend ? 'checked' : ''} /> Suspended (paused)</label>
          <div class="agents-form-actions"><button>Save changes</button><button type="button" class="secondary" data-trigcancel>Cancel</button></div>
        </form>`
      : `<form class="agents-trig-form">
          <h4>New trigger</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="on-issue" /></label>
            <label>Source<select name="source">${sourceOpts('webhook')}</select></label>
            <label>Connection<select name="connectionRef">${connOptions('')}</select></label>
          </div>
          <label>Task<textarea name="task" rows="2" placeholder="Triage the incoming GitHub issue and label it."></textarea></label>
          <button>Create trigger</button>
        </form>`
    return `
      <div class="agents-panel">
        <p class="muted">Run ${a.metadata.name} when an event happens. Webhook triggers get a hub-routed URL; github/channel triggers subscribe through a connection. Use ▶ to test, ✏️ to edit.</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Source</th><th>Connection</th><th>Webhook URL</th><th>Task</th><th class="agents-th-actions">Actions</th></tr></thead>
          <tbody>
            ${mine.length ? rows : `<tr class="agents-empty-row"><td colspan="6"><span class="agents-empty">⚡ No triggers yet — create one below.</span></td></tr>`}
          </tbody>
        </table>
        ${form}
      </div>`
  }
  private _wireTriggers(): void {
    this.querySelectorAll<HTMLElement>('[data-firetrig]').forEach((el) => el.addEventListener('click', () => void this._runTrigger(el.dataset.firetrig!)))
    this.querySelectorAll<HTMLElement>('[data-edittrig]').forEach((el) =>
      el.addEventListener('click', () => {
        this._trigEdit = el.dataset.edittrig!
        this._render()
      }),
    )
    this.querySelector<HTMLElement>('[data-trigcancel]')?.addEventListener('click', () => {
      this._trigEdit = null
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-suspendtrig]').forEach((el) =>
      el.addEventListener('click', () => {
        const on = el.dataset.suspend === '1'
        void this._updateTrigger(el.dataset.suspendtrig!, { suspend: on }, on ? 'Trigger paused.' : 'Trigger resumed.')
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-deltrig]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete trigger ${el.dataset.deltrig}?`)) void this._deleteTrigger(el.dataset.deltrig!)
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-copyhook]').forEach((el) =>
      el.addEventListener('click', () => {
        const url = location.origin + el.dataset.copyhook!
        void navigator.clipboard?.writeText(url)
        this._note = 'Webhook URL copied to clipboard.'
        this._render()
      }),
    )
    const f = this.querySelector<HTMLFormElement>('.agents-trig-form')
    f?.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
      const editName = f.dataset.edit
      if (editName) {
        const suspend = f.querySelector<HTMLInputElement>('[name=suspend]')?.checked || false
        void this._updateTrigger(editName, { source: g('source'), connectionRef: g('connectionRef'), task: g('task'), suspend })
      } else {
        void this._createTrigger({ name: g('name'), agentRef: this._selected, source: g('source'), connectionRef: g('connectionRef'), task: g('task') })
      }
    })
  }

  // ---- agent: channels -----------------------------------------------------

  private _renderChannels(a: Agent): string {
    const messaging = this._connections.filter((c) => ['telegram', 'slack', 'smtp', 'discord'].includes(c.spec.type))
    const notify = a.spec?.defaultNotifyConnection || ''
    const opts = `<option value="">— none —</option>` + messaging.map((c) => `<option value="${escapeHTML(c.metadata.name)}" ${c.metadata.name === notify ? 'selected' : ''}>${escapeHTML(c.metadata.name)} (${escapeHTML(c.spec.type)})</option>`).join('')
    const conn = messaging.find((c) => c.metadata.name === notify)
    return `
      <div class="agents-panel agents-form-panel">
        <p class="muted">This agent's channel is where scheduled/heartbeat output and approval requests are delivered — and, for Telegram/Slack, where you can chat with it inbound. Connections are shared; manage them under <strong>🔌 Connections</strong>.</p>
        <label>Notify / chat channel
          <select name="notify" data-set-notify>${opts}</select>
        </label>
        ${
          conn
            ? `<div class="agents-channel-actions">
                 ${['telegram', 'slack'].includes(conn.spec.type) ? `<button data-inbound="${escapeHTML(conn.metadata.name)}">Enable inbound chat</button>` : ''}
                 <button class="secondary" data-testconn="${escapeHTML(conn.metadata.name)}">Send test message</button>
               </div>
               ${conn.status?.webhookPath ? `<p class="muted">Inbound webhook: <code>${escapeHTML(conn.status.webhookPath)}</code> — from the channel use <code>/new</code>, <code>/status</code>, <code>/inbox</code>, <code>/approve N</code>.</p>` : ''}`
            : notify
              ? `<p class="muted">The selected connection no longer exists — pick another.</p>`
              : `<p class="muted">No channel set. Create a Telegram/Slack/SMTP connection first, then select it here.</p>`
        }
      </div>`
  }
  private _wireChannels(): void {
    this.querySelector<HTMLSelectElement>('[data-set-notify]')?.addEventListener('change', (e) => {
      void this._updateAgent({ notifyConnection: (e.target as HTMLSelectElement).value }, 'Channel updated.')
    })
    this.querySelectorAll<HTMLElement>('[data-inbound]').forEach((el) => el.addEventListener('click', () => void this._enableInbound(el.dataset.inbound!)))
    this.querySelectorAll<HTMLElement>('[data-testconn]').forEach((el) => el.addEventListener('click', () => void this._testConnection(el.dataset.testconn!)))
  }

  // ---- agent: settings -----------------------------------------------------

  private _renderSettings(a: Agent): string {
    const credOptions =
      `<option value="">— no model —</option>` +
      this._credentials.map((c) => `<option value="${escapeHTML(c.name)}" ${c.name === a.spec?.models?.chat ? 'selected' : ''}>${escapeHTML(c.name)}${c.model ? ` (${escapeHTML(c.model)})` : ''}</option>`).join('')
    const autonomy = a.spec?.autonomy || 'ask'
    const others = this._agents.filter((x) => x.metadata.name !== a.metadata.name)
    const delegates = new Set(a.spec?.delegates || [])
    return `
      <div class="agents-panel agents-form-panel">
        <form class="agents-settings-form">
          <label>Display name<input name="displayName" value="${escapeHTML(a.spec?.displayName || a.metadata.name)}" /></label>
          <label>Model credential
            <select name="modelCredential">${credOptions}</select>
            ${this._credentials.length === 0 ? `<span class="muted" style="font-size:12px">No models yet — add one under ⚙ Models.</span>` : ''}
          </label>
          <label>System prompt (persona + standing instructions)
            <textarea name="systemPrompt" rows="4" placeholder="You are a concise assistant that…">${escapeHTML(a.spec?.systemPrompt || '')}</textarea>
          </label>
          <div class="agents-grid2">
            <label>Autonomy
              <select name="autonomy">
                ${['suggest', 'ask', 'auto'].map((v) => `<option value="${v}" ${v === autonomy ? 'selected' : ''}>${v}</option>`).join('')}
              </select>
            </label>
            <label>Monthly budget (USD, blank = unlimited)
              <input name="budgetUSD" inputmode="decimal" value="${escapeHTML(a.spec?.budget?.usdLimit || '')}" placeholder="e.g. 20" />
            </label>
          </div>
          ${this._renderToolsMatrix(a)}
          ${
            others.length
              ? `<fieldset class="agents-delegates"><legend>Can delegate to</legend>${others
                  .map(
                    (o) =>
                      `<label class="agents-check"><input type="checkbox" name="delegate" value="${escapeHTML(o.metadata.name)}" ${delegates.has(o.metadata.name) ? 'checked' : ''} /> ${escapeHTML(o.metadata.name)}</label>`,
                  )
                  .join('')}</fieldset>`
              : ''
          }
          <div><button>Save settings</button></div>
        </form>
      </div>`
  }
  private _renderToolsMatrix(a: Agent): string {
    // core is always on (memory/notify/schedule/ask) — not shown as a toggle.
    const fams: { id: string; icon: string; label: string; desc: string }[] = [
      { id: 'web', icon: '🔧', label: 'Web search & fetch', desc: 'Search the web and read pages.' },
      { id: 'github', icon: '🐙', label: 'GitHub', desc: 'Issues, PRs, code — via github connections.' },
      { id: 'mcp', icon: '🔌', label: 'MCP servers', desc: 'Tools from your mcp connections.' },
      { id: 'edges', icon: '🌐', label: 'Cluster edges', desc: 'Act on Kubernetes edges as you.' },
    ]
    // Fall back to the same defaults the backend applies when unset, so the
    // checkboxes reflect what the agent actually gets today.
    const inter = new Set(a.spec?.tools?.interactive?.families || ['core', 'web', 'github', 'mcp', 'edges'])
    const bg = new Set(a.spec?.tools?.background?.families || ['core', 'web'])
    const rows = fams
      .map(
        (fam) => `<tr>
          <td><span class="agents-tool-name">${fam.icon} ${escapeHTML(fam.label)}</span><span class="agents-hint">${escapeHTML(fam.desc)}</span></td>
          <td class="agents-tool-cell"><input type="checkbox" name="tool-interactive" value="${fam.id}" ${inter.has(fam.id) ? 'checked' : ''} /></td>
          <td class="agents-tool-cell"><input type="checkbox" name="tool-background" value="${fam.id}" ${bg.has(fam.id) ? 'checked' : ''} /></td>
        </tr>`,
      )
      .join('')
    return `<fieldset class="agents-tools">
        <legend>Tools</legend>
        <p class="agents-hint">What ${escapeHTML(a.metadata.name)} can use. <strong>In chat</strong> = when you talk to it; <strong>In automation</strong> = crons &amp; triggers (kept tighter by default). Core memory/notify is always on. You still tell it what to do in the task, e.g. “search the web for X and summarize.”</p>
        <table class="agents-table agents-tools-table">
          <thead><tr><th>Capability</th><th class="agents-tool-cell">In chat</th><th class="agents-tool-cell">In automation</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </fieldset>`
  }
  private _wireSettings(): void {
    const f = this.querySelector<HTMLFormElement>('.agents-settings-form')
    f?.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
      const delegates = Array.from(f.querySelectorAll<HTMLInputElement>('input[name=delegate]:checked')).map((el) => el.value)
      const interactiveFamilies = Array.from(f.querySelectorAll<HTMLInputElement>('input[name=tool-interactive]:checked')).map((el) => el.value)
      const backgroundFamilies = Array.from(f.querySelectorAll<HTMLInputElement>('input[name=tool-background]:checked')).map((el) => el.value)
      void this._updateAgent({
        displayName: g('displayName'),
        modelCredential: g('modelCredential'),
        systemPrompt: g('systemPrompt'),
        autonomy: g('autonomy'),
        budgetUSD: g('budgetUSD'),
        delegates,
        interactiveFamilies,
        backgroundFamilies,
      })
    })
  }

  // ---- shared: models ------------------------------------------------------

  private _renderModels(): string {
    return `
      <div class="agents-panel agents-form-panel">
        <h3>Model credentials</h3>
        <p class="muted">Shared across the workspace — create once, assign to agents in each agent's Settings. Each is its own Secret (<code>kedge-agents-model-&lt;name&gt;</code>).</p>
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

  // ---- shared: connections -------------------------------------------------

  private _connFieldHTML(f: ConnField): string {
    return `<label>${escapeHTML(f.label)}${f.required ? ' *' : ''}
      <input name="${f.key}" ${f.password ? 'type="password"' : ''} placeholder="${escapeHTML(f.placeholder || '')}" ${f.required ? 'required' : ''} autocomplete="off" />
      ${f.hint ? `<span class="agents-hint">${escapeHTML(f.hint)}</span>` : ''}
    </label>`
  }
  private _renderConnEditForm(c: Connection): string {
    const cat = connCategory(c.spec.type)
    const usesChannel = cat === 'channel' || !!c.spec.channel
    // Discord is one backend type with two shapes: a webhook (channel is a
    // https:// URL, no secret) or a chat bot (channel is a numeric id or blank,
    // secret is the bot token). Detect so editing shows the right form.
    const isDiscord = c.spec.type === 'discord'
    const isDiscordWebhook = isDiscord && (c.spec.channel || '').startsWith('https://')
    const isDiscordBot = isDiscord && !isDiscordWebhook

    let endpointLabel: string
    if (isDiscordWebhook) endpointLabel = 'Webhook URL'
    else if (isDiscordBot) endpointLabel = 'Channel ID (optional)'
    else if (!usesChannel) endpointLabel = 'Endpoint URL'
    else if (c.spec.type === 'slack') endpointLabel = 'Webhook URL / channel'
    else if (c.spec.type === 'smtp') endpointLabel = 'Send to'
    else endpointLabel = 'Channel / chat ID'

    const endpointVal = usesChannel ? c.spec.channel || '' : c.spec.baseURL || ''
    const isOAuth = c.spec.auth === 'oauth'
    // Webhook discord has no secret; bot has a bot token.
    const secretField = isDiscordWebhook
      ? ''
      : isOAuth
        ? '<p class="agents-hint">This is an OAuth connection — use the 🔗 button in the table to re-authorize. Client credentials aren’t edited here.</p>'
        : `<label>New ${isDiscordBot ? 'bot token' : 'secret / token'}<input name="secret" type="password" placeholder="leave blank to keep the current one" /><span class="agents-hint">Only set this to rotate the credential.</span></label>`
    const kindLabel = isDiscordWebhook ? 'Discord webhook' : isDiscordBot ? 'Discord chat' : ''
    return `<form class="agents-conn-form" data-editconn="${escapeHTML(c.metadata.name)}" data-usechannel="${usesChannel ? '1' : '0'}">
        <div class="agents-conn-formhead">
          <button type="button" class="agents-back" data-conncancel>← connections</button>
          <h4>Edit ${escapeHTML(CATEGORY_META[cat].icon)} <code>${escapeHTML(c.metadata.name)}</code>${kindLabel ? ` <span class="agents-badge">${escapeHTML(kindLabel)}</span>` : ''}</h4>
        </div>
        <label>Display name<input name="displayName" value="${escapeHTML(c.spec.displayName || '')}" placeholder="${escapeHTML(c.metadata.name)}" /></label>
        <label>${endpointLabel}<input name="endpoint" value="${escapeHTML(endpointVal)}" /></label>
        ${secretField}
        <div class="agents-form-actions"><button>Save changes</button><button type="button" class="secondary" data-conncancel>Cancel</button></div>
      </form>`
  }
  private _renderConnForm(def: ConnTypeDef): string {
    const mode = this._connMode || def.modes?.[0].id || ''
    let fields = def.modes ? def.modes.find((m) => m.id === mode)!.fields : def.fields || []
    // Platform OAuth app configured (operator env, like the code provider)?
    // Then OAuth modes need no client id/secret — drop those fields.
    const isOAuthMode = fields.some((f) => f.key === 'clientID')
    let platformNote = ''
    if (isOAuthMode && this._oauthApps.has(def.id)) {
      fields = fields.filter((f) => f.key !== 'clientID' && f.key !== 'clientSecret')
      platformNote = `<div class="agents-platform-note">✓ Using the platform's ${escapeHTML(def.label)} OAuth app — no client id/secret needed. Create it, then click <strong>Connect</strong>.</div>`
    }
    return `
      <form class="agents-conn-form" data-type="${def.id}">
        <div class="agents-conn-formhead">
          <button type="button" class="agents-back" data-conntypes>← connection types</button>
          <h4>${def.glyph} ${escapeHTML(def.label)}</h4>
        </div>
        <p class="muted">${escapeHTML(def.desc)}</p>
        ${
          def.setup
            ? `<details class="agents-setup" open><summary>Before you start — setup steps</summary><ol>${def.setup.map((s) => `<li>${s}</li>`).join('')}</ol></details>`
            : ''
        }
        <label>Name *<input name="name" required pattern="[a-z0-9-]+" placeholder="my-${def.id}" /><span class="agents-hint">A short id you'll reference from agents.</span></label>
        ${
          def.modes
            ? `<div class="agents-modeseg">${def.modes.map((m) => `<button type="button" class="agents-modebtn ${m.id === mode ? 'sel' : ''}" data-connmode="${m.id}">${escapeHTML(m.label)}</button>`).join('')}</div>`
            : ''
        }
        ${platformNote}
        ${fields.map((f) => this._connFieldHTML(f)).join('')}
        ${def.advanced?.length ? `<details class="agents-adv"><summary>Advanced</summary>${def.advanced.map((f) => this._connFieldHTML(f)).join('')}</details>` : ''}
        <div><button>Create connection</button></div>
      </form>`
  }
  private _renderConnections(): string {
    const def = this._connType ? CONN_DEFS.find((d) => d.id === this._connType) : null
    const tile = (d: ConnTypeDef) => `<button class="agents-conn-tile" data-conntype="${d.id}">
                 <span class="agents-conn-glyph">${d.glyph}</span>
                 <span class="agents-conn-name">${escapeHTML(d.label)}</span>
                 <span class="muted">${escapeHTML(d.desc)}</span>
               </button>`
    const groups = (['tool', 'channel', 'connection'] as ConnCategory[])
      .map((cat) => {
        const defs = CONN_DEFS.filter((d) => connCategory(d.id) === cat)
        if (!defs.length) return ''
        const m = CATEGORY_META[cat]
        return `<div class="agents-conn-group">
            <h5 class="agents-conn-grouphead">${m.icon} ${escapeHTML(m.label)}s <span class="muted">— ${escapeHTML(m.blurb)}</span></h5>
            <div class="agents-conn-types">${defs.map(tile).join('')}</div>
          </div>`
      })
      .join('')
    const editConn = this._connEdit ? this._connections.find((c) => c.metadata.name === this._connEdit) : undefined
    const adder = editConn
      ? this._renderConnEditForm(editConn)
      : def
        ? this._renderConnForm(def)
        : `<div class="agents-conn-picker"><h4>Add a connection</h4>${groups}</div>`
    const catBadge = (id: string) => {
      const m = CATEGORY_META[connCategory(id)]
      return `<span class="agents-badge agents-badge-cat agents-cat-${connCategory(id)}">${m.icon} ${escapeHTML(m.label)}</span>`
    }
    // Discord is one backend type but two shapes — label it so the list reads clearly.
    const typeLabel = (c: Connection) => {
      if (c.spec.type !== 'discord') return c.spec.type
      return (c.spec.channel || '').startsWith('https://') ? 'discord webhook' : 'discord chat'
    }
    return `
      <div class="agents-panel">
        <h3>Connections</h3>
        <p class="muted">Shared credentials for external systems. Each is a 🔧 <strong>Tool</strong> agents call, a 📣 <strong>Channel</strong> they message you on, or a 🔌 generic <strong>Connection</strong>. Stored as Secrets in your workspace.</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Kind</th><th>Type</th><th>Endpoint / channel</th><th class="agents-th-actions">Actions</th></tr></thead>
          <tbody>
            ${
              this._connections.length
                ? this._connections
                    .map(
                      (c) => `<tr>
                        <td><span class="agents-cell-name">${escapeHTML(c.spec.displayName || c.metadata.name)}</span>${c.status?.webhookPath ? ' <span class="agents-inbound-on" title="Inbound enabled">⇄</span>' : ''}${c.status?.oauthConnected ? ' <span class="agents-inbound-on" title="OAuth connected">🔗</span>' : ''}</td>
                        <td>${catBadge(c.spec.type)}</td>
                        <td><span class="agents-badge">${escapeHTML(typeLabel(c))}</span></td>
                        <td class="agents-cell-task muted">${escapeHTML(c.spec.baseURL || c.spec.channel || '—')}</td>
                        <td class="agents-row-actions">
                          <button class="agents-iconbtn" data-editconn="${escapeHTML(c.metadata.name)}" title="Edit">✏️</button>
                          ${c.spec.auth === 'oauth' ? `<button class="agents-iconbtn" data-oauth="${escapeHTML(c.metadata.name)}" title="${c.status?.oauthConnected ? 'Reconnect OAuth' : 'Connect OAuth'}">🔗</button>` : ''}
                          <button class="agents-iconbtn agents-iconbtn-danger" data-delconn="${escapeHTML(c.metadata.name)}" title="Delete">🗑</button>
                        </td>
                      </tr>`,
                    )
                    .join('')
                : `<tr class="agents-empty-row"><td colspan="5"><span class="agents-empty">🔌 No connections yet — add one below.</span></td></tr>`
            }
          </tbody>
        </table>
        ${adder}
      </div>`
  }
  private _wireConnections(): void {
    this.querySelectorAll<HTMLElement>('[data-delconn]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete connection ${el.dataset.delconn}?`)) void this._deleteConnection(el.dataset.delconn!)
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-oauth]').forEach((el) => el.addEventListener('click', () => void this._oauthConnect(el.dataset.oauth!)))
    // Edit an existing connection.
    this.querySelectorAll<HTMLElement>('[data-editconn]:not(form)').forEach((el) =>
      el.addEventListener('click', () => {
        this._connEdit = el.dataset.editconn!
        this._connType = null
        this._render()
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-conncancel]').forEach((el) =>
      el.addEventListener('click', () => {
        this._connEdit = null
        this._render()
      }),
    )
    // Type picker → open that type's form.
    this.querySelectorAll<HTMLElement>('[data-conntype]').forEach((el) =>
      el.addEventListener('click', () => {
        this._connType = el.dataset.conntype!
        this._connMode = ''
        this._render()
      }),
    )
    this.querySelector<HTMLElement>('[data-conntypes]')?.addEventListener('click', () => {
      this._connType = null
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-connmode]').forEach((el) =>
      el.addEventListener('click', () => {
        this._connMode = el.dataset.connmode!
        this._render()
      }),
    )
    const f = this.querySelector<HTMLFormElement>('.agents-conn-form')
    if (!f) return
    // Edit-form submit (patch an existing connection).
    if (f.dataset.editconn) {
      f.addEventListener('submit', (e) => {
        e.preventDefault()
        const g = (n: string) => (f.querySelector<HTMLInputElement>(`[name=${n}]`)?.value || '').trim()
        const patch: Record<string, unknown> = { displayName: g('displayName') }
        if (f.dataset.usechannel === '1') patch.channel = g('endpoint')
        else patch.baseURL = g('endpoint')
        const secret = g('secret')
        if (secret) patch.secret = secret
        void this._updateConnection(f.dataset.editconn!, patch)
      })
      return
    }
    // Create-form submit.
    const def = this._connType ? CONN_DEFS.find((d) => d.id === this._connType) : null
    if (!def) return
    f.addEventListener('submit', (e) => {
      e.preventDefault()
      const v: Record<string, string> = {}
      f.querySelectorAll<HTMLInputElement>('input[name]').forEach((el) => (v[el.name] = el.value.trim()))
      const mode = this._connMode || def.modes?.[0].id || ''
      const body = def.build(v, mode)
      void this._createConnection(body).then(() => {
        this._connType = null
        this._connMode = ''
      })
    })
  }

  // ---- shared: inbox -------------------------------------------------------

  private _renderInbox(): string {
    return `
      <div class="agents-panel">
        <h3>Inbox</h3>
        <p class="muted">Approvals and questions your agents raise, across the workspace (also delivered to each agent's channel). Approve/deny grants one tool call.</p>
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
                        <td class="agents-row-actions">${i.state === 'pending' ? `<button data-approve="${escapeHTML(i.id)}">Approve</button><button class="secondary" data-deny="${escapeHTML(i.id)}">Deny</button>` : ''}</td>
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
}

function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string)
}

// midTrim keeps both ends of a long string with an ellipsis in the middle, so a
// webhook URL stays recognizable (host + token tail) without wrapping.
function midTrim(s: string, max = 40): string {
  if (s.length <= max) return s
  const keep = Math.floor((max - 1) / 2)
  return s.slice(0, keep) + '…' + s.slice(s.length - keep)
}
