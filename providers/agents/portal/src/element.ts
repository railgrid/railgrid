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
    tools?: {
      interactive?: { families?: string[]; toolsets?: string[]; connections?: string[] }
      background?: { families?: string[]; toolsets?: string[]; connections?: string[] }
    }
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

type AgentTab = 'chat' | 'overview' | 'settings'
type SharedView = 'models' | 'connections' | 'toolsets' | 'inbox'

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
  edges: 'tool',
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
  ['overview', 'Overview'],
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
  private _toolsetEdit: string | null = null
  private _inbox: InboxItem[] = []
  private _credentials: Credential[] = []
  private _connType: string | null = null
  private _connMode = ''
  private _connEdit: string | null = null
  private _oauthApps = new Set<string>()
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
    } else if (parts[0] === 'models' || parts[0] === 'connections' || parts[0] === 'toolsets' || parts[0] === 'inbox') {
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
      const panel =
        this._shared === 'models'
          ? this._renderModels()
          : this._shared === 'connections'
            ? this._renderConnections()
            : this._shared === 'toolsets'
              ? this._renderToolsets()
              : this._renderInbox()
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
    else if (this._shared === 'toolsets') this._wireToolsets()
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
            <button class="secondary" data-shared="toolsets">🧰 Toolsets${this._toolsets.length ? ` · ${this._toolsets.length}` : ''}</button>
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
    const listBody = flow ? '' : this._agentTab === 'chat' ? this._renderChat(a) : this._agentTab === 'overview' ? this._renderOverview(a) : this._renderSettings(a)
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
    else if (this._agentTab === 'overview') this._wireOverview()
    else this._wireSettings()
  }

  // ---- agent: overview (read + quick actions; edit in Flow) ----------------

  private _renderOverview(a: Agent): string {
    const name = a.metadata.name
    const scheds = this._schedules.filter((s) => s.spec.agentRef === name)
    const trigs = this._triggers.filter((t) => t.spec.agentRef === name)
    const model = a.spec?.models?.chat
    const notify = a.spec?.defaultNotifyConnection
    const directTools = Array.from(new Set([...(a.spec?.tools?.interactive?.connections || []), ...(a.spec?.tools?.background?.connections || [])]))
    const toolsets = Array.from(new Set([...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])]))
    const badge = (s: string) => `<span class="agents-badge">${escapeHTML(s)}</span>`
    const schedRows = scheds.length
      ? scheds
          .map((s) => {
            const when = s.spec.type === 'wakeup' ? s.spec.runAt || '' : s.spec.schedule || ''
            const next = s.status?.disabledReason ? `⚠ ${escapeHTML(s.status.disabledReason)}` : s.spec.suspend ? 'paused' : s.status?.nextRun ? 'next ' + this._fmtTime(s.status.nextRun) : 'armed'
            return `<tr><td><strong>${escapeHTML(s.metadata.name)}</strong></td><td class="mono">${escapeHTML(when)}</td><td class="muted">${next}</td>
              <td class="agents-row-actions"><button class="agents-iconbtn" data-runsched="${escapeHTML(s.metadata.name)}" title="Run now">▶</button><button class="agents-iconbtn agents-iconbtn-danger" data-delsched="${escapeHTML(s.metadata.name)}" title="Delete">🗑</button></td></tr>`
          })
          .join('')
      : `<tr><td colspan="4" class="muted">No schedules — add one in the Flow view.</td></tr>`
    const trigRows = trigs.length
      ? trigs
          .map(
            (t) =>
              `<tr><td><strong>${escapeHTML(t.metadata.name)}</strong></td><td class="mono">${escapeHTML(t.spec.source)}${t.spec.connectionRef ? ' · ' + escapeHTML(t.spec.connectionRef) : ''}</td><td class="muted">${t.spec.suspend ? 'paused' : t.status?.lastFired ? 'last ' + this._fmtTime(t.status.lastFired) : 'armed'}</td>
              <td class="agents-row-actions"><button class="agents-iconbtn" data-runtrig="${escapeHTML(t.metadata.name)}" title="Fire now">▶</button><button class="agents-iconbtn agents-iconbtn-danger" data-deltrig="${escapeHTML(t.metadata.name)}" title="Delete">🗑</button></td></tr>`,
          )
          .join('')
      : `<tr><td colspan="4" class="muted">No triggers — add one in the Flow view.</td></tr>`
    return `
      <div class="agents-page">
        <div class="agents-info">◆ Wiring is edited visually in the <strong>Flow</strong> view. <button class="agents-linkbtn" data-openflow>Open Flow</button></div>
        <div class="agents-overview-grid">
          <div class="agents-ov-card">
            <div class="agents-ov-k">Model</div>
            <div class="agents-ov-v ${model ? '' : 'warn'}">${model ? escapeHTML(model) : 'none set'}</div>
          </div>
          <div class="agents-ov-card">
            <div class="agents-ov-k">Notify channel</div>
            <div class="agents-ov-v ${notify ? '' : 'muted'}">${notify ? escapeHTML(notify) : 'none'}</div>
          </div>
          <div class="agents-ov-card">
            <div class="agents-ov-k">Autonomy</div>
            <div class="agents-ov-v">${escapeHTML(a.spec?.autonomy || 'ask')}</div>
          </div>
          <div class="agents-ov-card">
            <div class="agents-ov-k">Tools</div>
            <div class="agents-ov-v">${directTools.length ? directTools.map(badge).join(' ') : '<span class="muted">none wired</span>'}</div>
          </div>
          <div class="agents-ov-card">
            <div class="agents-ov-k">Toolsets</div>
            <div class="agents-ov-v">${toolsets.length ? toolsets.map(badge).join(' ') : '<span class="muted">none linked</span>'}</div>
          </div>
        </div>
        <h3>Schedules</h3>
        <table class="agents-table"><thead><tr><th>Name</th><th>When</th><th>Status</th><th class="agents-th-actions"></th></tr></thead><tbody>${schedRows}</tbody></table>
        <h3>Triggers</h3>
        <table class="agents-table"><thead><tr><th>Name</th><th>Source</th><th>Status</th><th class="agents-th-actions"></th></tr></thead><tbody>${trigRows}</tbody></table>
      </div>`
  }
  private _wireOverview(): void {
    this.querySelector<HTMLElement>('[data-openflow]')?.addEventListener('click', () => {
      this._agentView = 'flow'
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-runsched]').forEach((el) => el.addEventListener('click', () => void this._runSchedule(el.dataset.runsched!)))
    this.querySelectorAll<HTMLElement>('[data-delsched]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete schedule ${el.dataset.delsched}?`)) void this._deleteSchedule(el.dataset.delsched!)
      }),
    )
    this.querySelectorAll<HTMLElement>('[data-runtrig]').forEach((el) => el.addEventListener('click', () => void this._runTrigger(el.dataset.runtrig!)))
    this.querySelectorAll<HTMLElement>('[data-deltrig]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete trigger ${el.dataset.deltrig}?`)) void this._deleteTrigger(el.dataset.deltrig!)
      }),
    )
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
    // Tools: pick an existing tool of this type or create a new one — either
    // way it wires to the chosen target (this agent, or one of its toolsets).
    const toolKinds: Record<string, { type: string; label: string; mcp?: boolean }> = {
      'tool-mcp': { type: 'mcp', label: 'MCP', mcp: true },
      'tool-github': { type: 'github', label: 'GitHub' },
      'tool-web': { type: 'websearch', label: 'Web search' },
      'tool-edges': { type: 'edges', label: 'Cluster edges' },
    }
    if (key in toolKinds) {
      const t = toolKinds[key]
      const a = this._agent()
      const existing = this._connections.filter((c) => c.spec.type === t.type)
      const myToolsets = Array.from(new Set([...(a?.spec?.tools?.interactive?.toolsets || []), ...(a?.spec?.tools?.background?.toolsets || [])]))
      return {
        title: 'add ' + t.label.toLowerCase() + ' tool',
        nodeType: 'tool',
        ins: [],
        outs: ['events'],
        outPort: 'events',
        agentPort: 'tools',
        fields: [
          {
            key: 'existing',
            label: 'Tool',
            kind: 'select',
            value: '',
            options: [{ value: '', label: `＋ Create a new ${t.label} tool` }, ...existing.map((c) => ({ value: c.metadata.name, label: c.spec.displayName || c.metadata.name }))],
            hint: existing.length ? 'reuse one you already have, or create a new one below' : undefined,
          },
          {
            key: 'target',
            label: 'Add to',
            kind: 'select',
            value: 'agent',
            options: [{ value: 'agent', label: 'this agent' }, ...myToolsets.map((ts) => ({ value: 'toolset:' + ts, label: 'toolset · ' + ts }))],
          },
          { key: 'name', label: 'New tool name', kind: 'text', placeholder: 'lowercase-with-dashes', hint: 'only when creating a new tool' },
          ...(t.mcp ? [{ key: 'baseURL', label: 'MCP server URL', kind: 'text' as const, mono: true, placeholder: 'https://host/mcp' }] : []),
        ],
      }
    }
    if (key === 'toolset') {
      const a = this._agent()
      const mine = new Set([...(a?.spec?.tools?.interactive?.toolsets || []), ...(a?.spec?.tools?.background?.toolsets || [])])
      const unlinked = this._toolsets.filter((t) => !mine.has(t.metadata.name))
      return {
        title: 'add toolset',
        nodeType: 'toolset',
        ins: ['tool'],
        outs: ['use'],
        outPort: 'use',
        agentPort: 'tools',
        fields: [
          {
            key: 'existing',
            label: 'Toolset',
            kind: 'select',
            value: '',
            options: [{ value: '', label: '＋ Create a new toolset' }, ...unlinked.map((t) => ({ value: t.metadata.name, label: t.spec.displayName || t.metadata.name }))],
            hint: unlinked.length ? 'link a shared one, or create a new bundle below' : undefined,
          },
          { key: 'name', label: 'New toolset name', kind: 'text', placeholder: 'dev-tools', hint: 'only when creating a new toolset' },
          { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'optional' },
        ],
      }
    }
    return null // chat / tools (built-in) / notify / delegate are not standalone objects
  }

  // Write the object a draft describes; return its real flow-node id on success.
  private async _flowCreate(key: string, values: Record<string, string | string[]>): Promise<string | null> {
    const s = (k: string): string => String(values[k] ?? '').trim()
    const name = s('name')
    const agent = this._selected as string
    // Tools validate their own name (they may reuse an existing tool instead of
    // creating). Everything else needs a fresh name up front.
    if (!key.startsWith('tool-') && !/^[a-z0-9-]+$/.test(name)) {
      this._flow?.toast('Name must be lowercase letters, numbers and dashes')
      return null
    }
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
      // Tools are tool-type Connections. Pick an existing one or create a new
      // one, then wire it to the chosen target (agent or a toolset).
      const toolType: Record<string, string> = { 'tool-mcp': 'mcp', 'tool-github': 'github', 'tool-web': 'websearch', 'tool-edges': 'edges' }
      if (key in toolType) {
        let cn = s('existing')
        if (!cn) {
          if (!/^[a-z0-9-]+$/.test(name)) {
            this._flow?.toast('Pick an existing tool, or give the new one a name (a-z, 0-9, dashes)')
            return null
          }
          await this._send('POST', '/api/connections', { name, type: toolType[key], baseURL: s('baseURL') })
          await this._loadConnections()
          cn = name
        }
        await this._wireToolTo(s('target') || 'agent', cn)
        return 'conn:' + cn
      }
      if (key === 'toolset') {
        let tn = s('existing')
        if (!tn) {
          if (!/^[a-z0-9-]+$/.test(name)) {
            this._flow?.toast('Pick a toolset, or give the new one a name (a-z, 0-9, dashes)')
            return null
          }
          await this._send('POST', '/api/toolsets', { name, displayName: s('displayName'), families: ['core'] })
          await this._loadToolsets()
          tn = name
        }
        await this._linkToolset(agent, tn)
        await this._loadAgents()
        return 'toolset:' + tn
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

  // _wireToolTo attaches a tool connection to the agent (its own grant) or to
  // one of its toolsets, deriving families so the tool actually resolves.
  private async _wireToolTo(target: string, cn: string): Promise<void> {
    if (target.startsWith('toolset:')) {
      const tsName = target.slice(8)
      const ts = this._toolsets.find((x) => x.metadata.name === tsName)
      const conns = ts?.spec.connections || []
      if (!conns.includes(cn)) await this._updateToolset(tsName, { connections: [...conns, cn], families: this._familiesForConns([...conns, cn]) })
      return
    }
    const agent = this._selected as string
    const cur = this._agent()?.spec?.tools?.interactive?.connections || []
    if (cur.includes(cn)) return
    await this._send('PUT', `/api/agents/${encodeURIComponent(agent)}`, { interactiveConnections: [...cur, cn], interactiveFamilies: this._familiesForConns([...cur, cn]) })
    await this._loadAgents()
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
    const agentTools = new Set([...(a.spec?.tools?.interactive?.connections || []), ...(a.spec?.tools?.background?.connections || [])])
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

    // Toolsets: only the ones THIS agent links render (scoped to the agent).
    // Their tools are what makes those tools relevant to this canvas.
    const linked = new Set([...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])])
    const linkedToolsetConns = new Set<string>()
    for (const ts of this._toolsets) {
      const tn = ts.metadata.name
      if (!linked.has(tn)) continue
      const id = 'toolset:' + tn
      const tconns = ts.spec.connections || []
      tconns.forEach((c) => linkedToolsetConns.add(c))
      nodes.push({
        id,
        type: 'toolset',
        title: ts.spec.displayName || tn,
        ins: ['tool'],
        outs: ['use'],
        tags: tconns.length ? tconns : undefined,
        sub: ts.spec.description ? escapeHTML(ts.spec.description) : `<span class="mono">shared</span>${tconns.length ? ` · ${tconns.length} tool${tconns.length === 1 ? '' : 's'}` : ' · drag a tool in'}`,
        status: linked.has(tn) ? ['ok', 'linked'] : ['off', 'available'],
        canDelete: true,
        fields: [
          { key: 'displayName', label: 'Display name', kind: 'text', value: ts.spec.displayName || tn },
          { key: 'connections', label: 'Tools', kind: 'static', value: tconns.join(', ') || '— drag a tool here —' },
        ],
      })
      if (linked.has(tn)) wires.push({ from: [id, 'use'], to: ['agent', 'tools'] })
      for (const cn of tconns) if (this._connections.some((c) => c.metadata.name === cn)) wires.push({ from: ['conn:' + cn, 'events'], to: [id, 'tool'] })
    }

    // Only render connections relevant to THIS agent: its direct tools, its
    // linked toolsets' tools, trigger sources, and its notify channel. Other
    // workspace tools stay off the agent's canvas (no floating clutter).
    const showConn = new Set<string>([...agentTools, ...linkedToolsetConns])
    trigs.forEach((t) => t.spec.connectionRef && showConn.add(t.spec.connectionRef))
    if (notify) showConn.add(notify)

    // connections: tool-category ones render as Tool nodes; channel ones as
    // Connection nodes (notify sink + event source).
    for (const c of this._connections) {
      const cn = c.metadata.name
      if (!showConn.has(cn)) continue
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
      // Tool wired directly to the agent (its own grant, not via a toolset).
      if (isTool && agentTools.has(cn)) wires.push({ from: [id, 'events'], to: ['agent', 'tools'] })
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
      if ('displayName' in values) await this._updateToolset(id.slice(8), { displayName: str(values.displayName) })
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

  // Map a tool-type connection to the built-in family the backend uses to
  // resolve it. Families are never edited directly — they're derived from the
  // wired tools so the UI has just one concept: the Tool object.
  private static _TOOL_FAMILY: Record<string, string> = { mcp: 'mcp', github: 'github', websearch: 'web', edges: 'edges' }
  private _familiesForConns(names: string[]): string[] {
    const fams = new Set<string>(['core'])
    for (const n of names) {
      const c = this._connections.find((x) => x.metadata.name === n)
      const f = c && AgentsElement._TOOL_FAMILY[c.spec.type]
      if (f) fams.add(f)
    }
    return [...fams]
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
    // tool.events → toolset.tool : add the tool to the bundle. Families are
    // derived from the full connection list so they always match the wired tools.
    if (fromNode.startsWith('conn:') && toNode.startsWith('toolset:') && toPort === 'tool') {
      const tsName = toNode.slice(8)
      const cn = fromNode.slice(5)
      const ts = this._toolsets.find((x) => x.metadata.name === tsName)
      const conns = ts?.spec.connections || []
      if (conns.includes(cn)) return
      const next = [...conns, cn]
      return void this._updateToolset(tsName, { connections: next, families: this._familiesForConns(next) })
    }
    // tool.events → agent.tools : give the agent this tool directly (its own
    // grant), deriving the family from the tool.
    if (fromNode.startsWith('conn:') && toNode === 'agent' && toPort === 'tools') {
      const cn = fromNode.slice(5)
      const a = this._agent()
      const cur = a?.spec?.tools?.interactive?.connections || []
      if (cur.includes(cn)) return
      const next = [...cur, cn]
      return void this._updateAgent({ interactiveConnections: next, interactiveFamilies: this._familiesForConns(next) }, 'Tool added to agent.')
    }
    this._flow?.toast('These ports don’t connect — try tool → toolset or agent, toolset → agent, or schedule/trigger → agent.')
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
          <fieldset class="agents-tools"><legend>Tools</legend>
            <p class="agents-hint">Tools &amp; toolsets are wired in the <strong>Flow</strong> view. See the <strong>Overview</strong> tab for a summary.</p>
          </fieldset>
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
  private _wireSettings(): void {
    const f = this.querySelector<HTMLFormElement>('.agents-settings-form')
    f?.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
      const delegates = Array.from(f.querySelectorAll<HTMLInputElement>('input[name=delegate]:checked')).map((el) => el.value)
      // Tool families/toolsets are edited in Flow — not sent here, so this save
      // never clobbers them.
      void this._updateAgent({
        displayName: g('displayName'),
        modelCredential: g('modelCredential'),
        systemPrompt: g('systemPrompt'),
        autonomy: g('autonomy'),
        budgetUSD: g('budgetUSD'),
        delegates,
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

  // ---- shared: toolsets ----------------------------------------------------

  private _renderToolsets(): string {
    const toolConns = this._connections.filter((c) => CONN_CATEGORY[c.spec.type] === 'tool')
    const usedByCount = (name: string): number =>
      this._agents.filter((a) => [...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])].includes(name)).length
    const rows =
      this._toolsets.length === 0
        ? `<tr><td colspan="4" class="muted">No toolsets yet — create one below or in the Flow view.</td></tr>`
        : this._toolsets
            .map((t) => {
              const conns = t.spec.connections || []
              const used = usedByCount(t.metadata.name)
              return `<tr>
                <td><strong>${escapeHTML(t.spec.displayName || t.metadata.name)}</strong>${t.spec.displayName ? `<span class="agents-hint"> ${escapeHTML(t.metadata.name)}</span>` : ''}</td>
                <td>${conns.length ? conns.map((c) => `<span class="agents-badge">${escapeHTML(c)}</span>`).join(' ') : '<span class="muted">—</span>'}</td>
                <td class="muted">${used} agent${used === 1 ? '' : 's'}</td>
                <td class="agents-row-actions">
                  <button class="agents-iconbtn" data-edittoolset="${escapeHTML(t.metadata.name)}" title="Edit">✏️</button>
                  <button class="agents-iconbtn agents-iconbtn-danger" data-deltoolset="${escapeHTML(t.metadata.name)}" title="Delete">🗑</button>
                </td>
              </tr>`
            })
            .join('')
    const editing = this._toolsets.find((t) => t.metadata.name === this._toolsetEdit)
    const connChecks = (on: Set<string>): string =>
      toolConns.length
        ? toolConns
            .map(
              (c) => `<label class="agents-check"><input type="checkbox" name="connection" value="${escapeHTML(c.metadata.name)}" ${on.has(c.metadata.name) ? 'checked' : ''} /> ${escapeHTML(c.metadata.name)} <span class="agents-hint">${escapeHTML(c.spec.type)}</span></label>`,
            )
            .join('')
        : `<span class="muted">No tools yet — create MCP/GitHub/web/edges tools in the Flow view.</span>`
    const form = editing
      ? `<form class="agents-toolset-form" data-edit="${escapeHTML(editing.metadata.name)}">
          <h4>Edit toolset <code>${escapeHTML(editing.metadata.name)}</code></h4>
          <label>Display name<input name="displayName" value="${escapeHTML(editing.spec.displayName || '')}" /></label>
          <fieldset class="agents-tools"><legend>Tools</legend><div class="agents-checkrow">${connChecks(new Set(editing.spec.connections || []))}</div></fieldset>
          <div class="agents-form-actions"><button>Save</button><button type="button" class="secondary" data-toolsetcancel>Cancel</button></div>
        </form>`
      : `<form class="agents-toolset-form">
          <h4>New toolset</h4>
          <div class="agents-grid2">
            <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="dev-tools" /></label>
            <label>Display name<input name="displayName" placeholder="optional" /></label>
          </div>
          <fieldset class="agents-tools"><legend>Tools</legend><div class="agents-checkrow">${connChecks(new Set())}</div></fieldset>
          <button>Create toolset</button>
        </form>`
    return `
      <div class="agents-panel agents-form-panel">
        <h3>Toolsets</h3>
        <p class="muted">Shared bundles of Tools that agents link. Define once, attach to many agents (in each agent's Flow, drag the toolset onto the agent).</p>
        <table class="agents-table">
          <thead><tr><th>Name</th><th>Tools</th><th>Used by</th><th class="agents-th-actions">Actions</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
        ${form}
      </div>`
  }
  private _wireToolsets(): void {
    this.querySelectorAll<HTMLElement>('[data-edittoolset]').forEach((el) =>
      el.addEventListener('click', () => {
        this._toolsetEdit = el.dataset.edittoolset!
        this._render()
      }),
    )
    this.querySelector<HTMLElement>('[data-toolsetcancel]')?.addEventListener('click', () => {
      this._toolsetEdit = null
      this._render()
    })
    this.querySelectorAll<HTMLElement>('[data-deltoolset]').forEach((el) =>
      el.addEventListener('click', () => {
        if (confirm(`Delete toolset ${el.dataset.deltoolset}? Agents linking it will lose those tools.`)) void this._deleteToolset(el.dataset.deltoolset!)
      }),
    )
    const form = this.querySelector<HTMLFormElement>('.agents-toolset-form')
    form?.addEventListener('submit', (e) => {
      e.preventDefault()
      const connections = Array.from(form.querySelectorAll<HTMLInputElement>('input[name=connection]:checked')).map((i) => i.value)
      const families = this._familiesForConns(connections) // derived, never hand-picked
      const displayName = (form.querySelector<HTMLInputElement>('input[name=displayName]')?.value || '').trim()
      const editName = form.dataset.edit
      if (editName) {
        void this._updateToolset(editName, { displayName, families, connections })
        this._toolsetEdit = null
      } else {
        const name = (form.querySelector<HTMLInputElement>('input[name=name]')?.value || '').trim()
        if (name) void this._createToolset({ name, displayName, families, connections })
      }
    })
  }
  private async _createToolset(body: Record<string, unknown>): Promise<void> {
    try {
      await this._send('POST', '/api/toolsets', body)
      this._note = 'Toolset created.'
      await this._loadToolsets()
    } catch (e) {
      this._note = 'Create failed: ' + (e as Error).message
      this._render()
    }
  }
  private async _deleteToolset(name: string): Promise<void> {
    try {
      await this._send('DELETE', `/api/toolsets/${encodeURIComponent(name)}`)
      await this._loadToolsets()
    } catch (e) {
      this._note = 'Delete failed: ' + (e as Error).message
      this._render()
    }
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
                          ${connCategory(c.spec.type) === 'channel' ? `<button class="agents-iconbtn" data-testconn="${escapeHTML(c.metadata.name)}" title="Send a test message">📨</button>` : ''}
                          ${connCategory(c.spec.type) === 'channel' ? `<button class="agents-iconbtn" data-inbound="${escapeHTML(c.metadata.name)}" title="${c.status?.webhookPath ? 'Inbound enabled' : 'Enable inbound chat'}">⇄</button>` : ''}
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
    this.querySelectorAll<HTMLElement>('[data-testconn]').forEach((el) => el.addEventListener('click', () => void this._testConnection(el.dataset.testconn!)))
    this.querySelectorAll<HTMLElement>('[data-inbound]').forEach((el) => el.addEventListener('click', () => void this._enableInbound(el.dataset.inbound!)))
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

