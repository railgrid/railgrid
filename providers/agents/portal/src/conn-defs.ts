// Static definitions for the model + connection create forms. Connections are
// TYPE-DRIVEN: pick what you're connecting, then a form with only that type's
// fields, each labelled with where to get the value.

import type { IconName } from './portalkit/icons'
import {
  MODEL_PROVIDER_CLAUDE_CODE,
  MODEL_PROVIDER_CODEX,
  MODEL_PROVIDER_OPENAI,
  MODEL_PROVIDER_OPENAI_COMPATIBLE,
  isHarnessProvider,
  type Connection,
  type ConnectionWrite,
} from './types'

/**
 * A model credential belongs to one of two FAMILIES, and they are not variants
 * of each other (apis/v1alpha1/types_modelcredential.go):
 *
 *   - "chat": a base URL plus a key, probed with GET /models, turned into a
 *     chat model by llm.BuildModel.
 *   - "harness": the login a coding harness on an edge runs as. No endpoint,
 *     nothing to probe, no chat model — the value is handed to the harness for
 *     one turn.
 *
 * The family is what decides which FIELDS a credential has, which is why the
 * preset carries it: the editor branches on this, not on a list of provider
 * names spelled out a second time.
 */
export type ProviderFamily = 'chat' | 'harness'

export interface ProviderPreset {
  id: string
  label: string
  /** The spec.provider value this preset writes. */
  provider: string
  family: ProviderFamily
  /** Chat endpoints only. A harness identity has no endpoint at all. */
  baseURL: string
  modelHint: string
  /** The one line under the Provider select. */
  guidance: string
  /**
   * Harness identities only: where the value comes from. One line, because the
   * model itself is documented in docs/edge-harness.md and a form is not the
   * place to repeat it.
   */
  origin?: string
}

// The preset list is the provider CHOICE. It used to be a base-URL shortcut
// with spec.provider pinned to openai-compatible, which is why two of the
// four providers the CRD accepts could not be created here at all.
export const PROVIDER_PRESETS: ProviderPreset[] = [
  {
    id: 'openai',
    label: 'OpenAI',
    provider: MODEL_PROVIDER_OPENAI,
    family: 'chat',
    baseURL: 'https://api.openai.com/v1',
    modelHint: 'gpt-4o',
    guidance: 'Uses OpenAI’s standard API endpoint.',
  },
  {
    id: 'anthropic',
    label: 'Anthropic (Claude, OpenAI-compat)',
    provider: MODEL_PROVIDER_OPENAI_COMPATIBLE,
    family: 'chat',
    baseURL: 'https://api.anthropic.com/v1',
    modelHint: 'claude-sonnet-4-20250514',
    guidance: 'Use a provider or gateway that implements OpenAI Chat Completions and GET /models.',
  },
  {
    id: 'openrouter',
    label: 'OpenRouter',
    provider: MODEL_PROVIDER_OPENAI_COMPATIBLE,
    family: 'chat',
    baseURL: 'https://openrouter.ai/api/v1',
    modelHint: 'anthropic/claude-sonnet-4',
    guidance: 'Use a provider or gateway that implements OpenAI Chat Completions and GET /models.',
  },
  {
    id: 'custom',
    label: 'Custom OpenAI-compatible',
    provider: MODEL_PROVIDER_OPENAI_COMPATIBLE,
    family: 'chat',
    baseURL: '',
    modelHint: 'model-name',
    guidance: 'Use a provider or gateway that implements OpenAI Chat Completions and GET /models.',
  },
  {
    id: 'claude-code',
    label: 'Claude Code (harness identity)',
    provider: MODEL_PROVIDER_CLAUDE_CODE,
    family: 'harness',
    baseURL: '',
    modelHint: '',
    guidance: 'The login a Claude Code harness on an edge runs as. There is no endpoint: the value is handed to the harness for one turn.',
    origin: '`claude setup-token` prints a long-lived token tied to a Claude subscription; an Anthropic API key bills that account instead.',
  },
  {
    id: 'codex',
    label: 'Codex (harness identity)',
    provider: MODEL_PROVIDER_CODEX,
    family: 'harness',
    baseURL: '',
    modelHint: '',
    guidance: 'The login a Codex harness on an edge runs as. There is no endpoint: the value is handed to the harness for one turn.',
    origin: '`codex login` on a machine you control writes an auth.json; paste its contents.',
  },
]

/** The preset a new credential opens in. */
export const DEFAULT_PROVIDER_PRESET = PROVIDER_PRESETS[0]

/** providerPreset resolves a preset id, falling back to the default. */
export function providerPreset(id: string): ProviderPreset {
  return PROVIDER_PRESETS.find(item => item.id === id) || DEFAULT_PROVIDER_PRESET
}

/**
 * presetFor is the preset a STORED credential opens in.
 *
 * A harness identity is recognized by its provider, because it has no endpoint
 * to recognize it by. A chat endpoint is still recognized by its base URL,
 * which is how this form has always restored a preset — two chat presets share
 * one provider value, so the URL is the only thing that tells them apart.
 */
export function presetFor(credential: { provider?: string; baseURL?: string } | undefined): ProviderPreset {
  if (!credential) return DEFAULT_PROVIDER_PRESET
  if (isHarnessProvider(credential.provider)) {
    return PROVIDER_PRESETS.find(item => item.provider === credential.provider) || DEFAULT_PROVIDER_PRESET
  }
  const url = (credential.baseURL || '').trim()
  // No URL on a chat credential means nothing was stored, so the form opens on
  // the default endpoint rather than on "custom" with an empty box.
  if (!url) return DEFAULT_PROVIDER_PRESET
  return PROVIDER_PRESETS.find(item => item.family === 'chat' && item.baseURL === url) || providerPreset('custom')
}

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
  // Mode-specific advanced fields, merged with the type's own when this mode
  // is selected (a self-hosted backend needs knobs the hosted one does not).
  advanced?: ConnField[]
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
  build: (v: Record<string, string>, mode: string) => ConnectionWrite
}

// Slack signs every Events API request with the app's signing secret; the
// provider refuses inbound events it cannot verify, so a Slack connection
// that should receive chat needs it. Shared by the bot-token and OAuth modes
// and by the edit form (existing connections add it there).
export const SLACK_SIGNING_SECRET_FIELD: ConnField = {
  key: 'signingSecret',
  label: 'Signing secret',
  password: true,
  hint: 'Slack app → Basic Information → App Credentials → Signing Secret. Required for inbound chat (Events API); the provider verifies every request with it.',
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

// Discord is ONE backend type with two shapes: a webhook (channel is an
// https:// URL, no secret — outbound only) or a chat bot (channel is a numeric
// id or blank, secret is the bot token). Every place that renders or edits a
// Discord connection needs the distinction, so it is derived here once.
export interface ConnShape {
  discordWebhook: boolean
  discordBot: boolean
  // typeLabel is what the UI calls this connection ("discord chat" vs the bare
  // spec.type for everything else).
  typeLabel: string
}

export function connShape(c: Pick<Connection, 'spec'>): ConnShape {
  const isDiscord = c.spec.type === 'discord'
  const discordWebhook = isDiscord && (c.spec.channel || '').startsWith('https://')
  const discordBot = isDiscord && !discordWebhook
  return {
    discordWebhook,
    discordBot,
    typeLabel: discordWebhook ? 'discord webhook' : discordBot ? 'discord chat' : c.spec.type,
  }
}

/** Webhook URLs are credentials: show their configured state, never the value. */
export function isSecretBearingWebhook(c: Pick<Connection, 'spec'>): boolean {
  const channel = (c.spec.channel || '').trim().toLowerCase()
  return (c.spec.type === 'slack' && channel.startsWith('https://hooks.slack.com/')) || connShape(c).discordWebhook
}

export interface InboundState {
  on: boolean
  canEnable: boolean
  note: string
}

// channelInbound reports whether a channel Connection can receive messages for
// an agent. Chat-capable: telegram/slack (webhook inbound) and the Discord bot;
// send-only otherwise. An agent receives on every channel it lists, so any
// bound chat-capable channel is a receiver.
export function channelInbound(c: Pick<Connection, 'spec' | 'status'>): InboundState {
  const shape = connShape(c)
  const canReceive = c.spec.type === 'telegram' || c.spec.type === 'slack' || shape.discordBot
  if (!canReceive) return { on: false, canEnable: false, note: 'Send-only — this channel can notify you, but can’t receive chat.' }
  if (shape.discordBot) return { on: true, canEnable: false, note: 'Inbound is automatic — the Discord bot delivers messages while linked.' }
  if (c.status?.webhookPath) return { on: true, canEnable: false, note: 'Receiving — messages from this channel reach the agent.' }
  return { on: false, canEnable: true, note: 'Not receiving yet — enable inbound to register the webhook.' }
}

// Map a tool-type connection to the built-in family the backend uses to resolve
// it. Families are never edited directly — they're derived from the wired tools
// so the UI has just one concept: the Tool object.
const TOOL_FAMILY: Record<string, string> = { mcp: 'mcp', github: 'github', websearch: 'web', edges: 'edges' }

// Families that are NOT derived from a connection — they are capabilities of the
// agent itself, toggled directly. Because familiesForConns rebuilds the list
// from scratch on every tool grant, these have to be carried over explicitly or
// wiring a tool would silently switch them off.
//
// "web" belongs here even though a websearch Connection also implies it:
// web_fetch needs no connection whatsoever (it reads public URLs), so an agent
// can usefully have web without one — and a preset that grants web would
// otherwise lose it the moment the user wired any tool.
export const STANDALONE_FAMILIES = ['spawn', 'web', 'visualization'] as const

export function familiesForConns(
  names: string[],
  connType: (name: string) => string | undefined,
  current: string[] = [],
): string[] {
  const fams = new Set<string>(['core'])
  for (const f of STANDALONE_FAMILIES) if (current.includes(f)) fams.add(f)
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
      const b: ConnectionWrite = { type: 'github', name: v.name }
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
    desc: 'A workload in your workspace, or any external Model Context Protocol server',
    setup: [
      'A <strong>workload in this workspace</strong> — the <code>browser</code> template, say — is named, not addressed: agents reach it over the platform’s internal path, so it is never published and needs no token.',
      'An <strong>external server</strong> takes its streamable-HTTP URL and, if it authenticates, a bearer token.',
    ],
    modes: [
      {
        id: 'instance',
        label: 'Workload in this workspace',
        fields: [
          {
            key: 'instance',
            label: 'Instance name',
            required: true,
            placeholder: 'browser',
            hint: 'The instance under Infrastructure. No URL, no token — access is authorized by your own permission on it.',
          },
        ],
        advanced: [
          {
            key: 'instanceResource',
            label: 'Instance resource',
            placeholder: 'instances',
            hint: 'Only if the instance is served by a provider under a different resource than the standard flattened "instances".',
          },
        ],
      },
      {
        id: 'external',
        label: 'External server',
        fields: [
          { key: 'baseURL', label: 'Server endpoint', required: true, placeholder: 'https://example.com/mcp', hint: 'The server’s streamable-HTTP MCP URL.' },
          { key: 'token', label: 'Bearer token', password: true, hint: 'Only if the server requires authentication.' },
        ],
      },
    ],
    build: (v, mode): ConnectionWrite =>
      mode === 'external'
        ? { type: 'mcp', name: v.name, baseURL: v.baseURL, secret: v.token || undefined }
        : { type: 'mcp', name: v.name, config: { instance: v.instance, ...(v.instanceResource ? { instanceResource: v.instanceResource } : {}) } },
  },
  {
    id: 'websearch',
    label: 'Web search',
    glyph: 'search',
    desc: 'Give agents web_search — your own SearXNG instance, or a Brave API key',
    setup: [
      'Self-hosted is the default — no API key, no per-query bill. Provision the <strong>searxng</strong> template under Infrastructure, then name that instance here.',
      'Agents reach the instance over the platform’s internal path (the infrastructure provider’s data plane), so it is never published to the internet and there is no URL or token to copy around. Access is authorized by your own permission on the instance.',
      'Brave is the alternative if you would rather not run anything — it needs a free-tier API key from <code>api.search.brave.com/app/keys</code>.',
    ],
    modes: [
      {
        id: 'searxng',
        label: 'Self-hosted (SearXNG)',
        fields: [
          {
            key: 'instance',
            label: 'Instance name',
            required: true,
            placeholder: 'search',
            hint: 'The name of your searxng instance under Infrastructure. Nothing else is needed — no URL, no token.',
          },
        ],
      },
      {
        id: 'brave',
        label: 'Brave API',
        fields: [{ key: 'token', label: 'API key', password: true, required: true, hint: 'Brave Search API key — api.search.brave.com/app/keys (free tier available).' }],
        advanced: [{ key: 'baseURL', label: 'Custom endpoint', placeholder: 'https://api.search.brave.com/res/v1/web/search' }],
      },
    ],
    build: (v, mode): ConnectionWrite => ({
      type: 'websearch',
      name: v.name,
      secret: mode === 'brave' ? v.token : undefined,
      baseURL: mode === 'brave' ? v.baseURL || undefined : undefined,
      config: mode === 'brave' ? { provider: 'brave' } : { provider: 'searxng', instance: v.instance },
    }),
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
          SLACK_SIGNING_SECRET_FIELD,
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
          SLACK_SIGNING_SECRET_FIELD,
        ],
      },
    ],
    build: (v, mode) => {
      const b: ConnectionWrite = { type: 'slack', name: v.name }
      if (mode === 'webhook') b.channel = v.channel
      else if (mode === 'oauth') {
        b.auth = 'oauth'
        b.oauthProvider = 'slack'
        b.clientID = v.clientID
        b.clientSecret = v.clientSecret
        b.channel = v.channel
        if (v.scopes) b.oauthScopes = v.scopes.trim().split(/\s+/)
        if (v.signingSecret) b.signingSecret = v.signingSecret
      } else {
        b.secret = v.token
        b.channel = v.channel
        if (v.signingSecret) b.signingSecret = v.signingSecret
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
