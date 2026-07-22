// Static definitions for the model + connection create forms. Connections are
// TYPE-DRIVEN: pick what you're connecting, then a form with only that type's
// fields, each labelled with where to get the value.

import type { IconName } from './portalkit/icons'

export const PROVIDER_PRESETS: { id: string; label: string; baseURL: string; modelHint: string }[] = [
  { id: 'openai', label: 'OpenAI', baseURL: 'https://api.openai.com/v1', modelHint: 'gpt-4o' },
  { id: 'anthropic', label: 'Anthropic (Claude, OpenAI-compat)', baseURL: 'https://api.anthropic.com/v1', modelHint: 'claude-sonnet-4-20250514' },
  { id: 'openrouter', label: 'OpenRouter', baseURL: 'https://openrouter.ai/api/v1', modelHint: 'anthropic/claude-sonnet-4' },
  { id: 'custom', label: 'Custom (OpenAI-compatible)', baseURL: '', modelHint: 'model-name' },
]

export interface ConnField {
  key: string
  label: string
  hint?: string
  placeholder?: string
  password?: boolean
  required?: boolean
}
export interface ConnMode {
  id: string
  label: string
  fields: ConnField[]
}
export interface ConnTypeDef {
  id: string
  label: string
  glyph: IconName
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
export type ConnCategory = 'tool' | 'channel' | 'connection'
export const CONN_CATEGORY: Record<string, ConnCategory> = {
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
export const CATEGORY_META: Record<ConnCategory, { icon: IconName; label: string; blurb: string }> = {
  tool: { icon: 'wrench', label: 'Tool', blurb: 'Capabilities agents call during a run.' },
  channel: { icon: 'megaphone', label: 'Channel', blurb: 'Where agents message you — notify + inbound chat.' },
  connection: { icon: 'plug', label: 'Connection', blurb: 'Generic API credentials for custom integrations.' },
}
export function connCategory(id: string): ConnCategory {
  return CONN_CATEGORY[id] || 'connection'
}

// Map a tool-type connection to the built-in family the backend uses to resolve
// it. Families are never edited directly — they're derived from the wired tools
// so the UI has just one concept: the Tool object.
const TOOL_FAMILY: Record<string, string> = { mcp: 'mcp', github: 'github', websearch: 'web', edges: 'edges' }
export function familiesForConns(names: string[], connType: (name: string) => string | undefined): string[] {
  const fams = new Set<string>(['core'])
  for (const n of names) {
    const t = connType(n)
    const f = t && TOOL_FAMILY[t]
    if (f) fams.add(f)
  }
  return [...fams]
}

export const CONN_DEFS: ConnTypeDef[] = [
  {
    id: 'github',
    label: 'GitHub',
    glyph: 'github',
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
    glyph: 'puzzle',
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
    glyph: 'search',
    desc: 'Give agents web_search (Brave-compatible API)',
    fields: [{ key: 'token', label: 'API key', password: true, required: true, hint: 'Brave Search API key — api.search.brave.com/app/keys (free tier available).' }],
    advanced: [{ key: 'baseURL', label: 'Custom endpoint', placeholder: 'https://api.search.brave.com/res/v1/web/search' }],
    build: (v) => ({ type: 'websearch', name: v.name, secret: v.token, baseURL: v.baseURL || undefined }),
  },
  {
    id: 'telegram',
    label: 'Telegram',
    glyph: 'send',
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
    glyph: 'message',
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
    glyph: 'discord',
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
    glyph: 'megaphone',
    desc: 'Notify a Discord channel (outbound only)',
    fields: [
      { key: 'channel', label: 'Webhook URL', required: true, hint: 'Channel → Edit Channel → Integrations → Webhooks → New Webhook → Copy URL. Outbound only, no chat.' },
    ],
    build: (v) => ({ type: 'discord', name: v.name, channel: v.channel }),
  },
  {
    id: 'smtp',
    label: 'Email (SMTP)',
    glyph: 'mail',
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
    glyph: 'globe',
    desc: 'Generic HTTP endpoint',
    fields: [
      { key: 'baseURL', label: 'Base URL', required: true, placeholder: 'https://api.example.com' },
      { key: 'token', label: 'Bearer token', password: true },
    ],
    build: (v) => ({ type: 'http', name: v.name, baseURL: v.baseURL, secret: v.token || undefined }),
  },
]
