// Agent flow tab: mounts the FlowCanvas node-graph for one agent and feeds it a
// model derived from live data. The canvas is imperative and survives the
// shell's full re-render — the instance + its root live at module scope, and the
// shell re-attaches the root into the freshly-rendered [data-flow-host] each
// time. The palette lists the workspace's EXISTING objects grouped by type;
// dragging one references it to this agent (addExisting), and each group has a
// "＋ new" create entry (draftFor/create).

import { confirmModal } from '../portalkit/modal'
import { FlowCanvas } from '../flow'
import type { FlowModel, FNode, FWire, FlowCallbacks, DraftSpec, PaletteGroup, PaletteEntry } from '../flow'
import type { ViewCtx } from '../view'
import type { Agent } from '../types'
import { escapeHTML, fmtTime } from '../types'
import { CONN_CATEGORY, connCategory } from '../conn-defs'
import {
  updateAgent,
  updateSchedule,
  updateTrigger,
  updateToolset,
  updateConnection,
  deleteSchedule,
  deleteTrigger,
  runSchedule,
  runTrigger,
  linkToolset,
  wireToolTo,
  enableInbound,
} from '../actions'

let flow: FlowCanvas | null = null
let flowRoot: HTMLElement | null = null
// The active (vc, agent) the canvas callbacks read from. Refreshed on every
// mount so a long-lived callbacks object always sees the current context.
let cur: { vc: ViewCtx; agent: string } | null = null

// mount attaches the canvas into the flow host and pushes a fresh model. Called
// after the agent-detail view renders its flow tab.
export function mount(vc: ViewCtx, root: HTMLElement, agentName: string): void {
  cur = { vc, agent: agentName }
  const host = root.querySelector<HTMLElement>('[data-flow-host]')
  if (!host) return
  if (!flow || !flowRoot) {
    flowRoot = document.createElement('div')
    flow = new FlowCanvas(flowRoot, callbacks())
  }
  host.appendChild(flowRoot)
  flow.update(flowModel(vc, agentName))
}

function callbacks(): FlowCallbacks {
  return {
    onEdit: (id, values) => void flowEdit(id, values),
    onLink: (from, to) => void flowLink(from, to),
    onRun: (id) => void flowRun(id),
    onDelete: (id) => void flowDelete(id),
    onOpenChat: () => cur && cur.vc.navigate({ kind: 'agent', name: cur.agent, tab: 'chat' }),
    onToast: (m) => flow?.toast(m),
    onEnableInbound: (id) => void flowEnableInbound(id),
    draftFor: (t) => flowDraftFor(t),
    create: (t, values) => flowCreate(t, values),
    addExisting: (id) => flowAddExisting(id),
  }
}

// ---- model derivation ------------------------------------------------------

function flowModel(vc: ViewCtx, name: string): FlowModel {
  const a = vc.store.agent(name)
  if (!a) return { key: name, nodes: [], wires: [], palette: [] }
  const nodes: FNode[] = []
  const wires: FWire[] = []
  const scheds = vc.store.schedules.filter((s) => s.spec.agentRef === name)
  const trigs = vc.store.triggers.filter((t) => t.spec.agentRef === name)
  const model = a.spec?.models?.chat
  const fallbacks = a.spec?.modelFallbacks || []
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
      status: dis ? ['off', dis] : s.spec.suspend ? ['off', 'paused'] : ['ok', s.status?.nextRun ? 'next ' + fmtTime(s.status.nextRun) : 'armed'],
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
      status: t.spec.suspend ? ['off', 'paused'] : ['ok', t.status?.lastFired ? 'last ' + fmtTime(t.status.lastFired) : 'armed'],
      canRun: true,
      canDelete: true,
      fields: [
        { key: 'source', label: 'Source', kind: 'select', value: t.spec.source, options: ['webhook', 'github'].map((v) => ({ value: v, label: v })) },
        {
          key: 'connectionRef',
          label: 'Connection',
          kind: 'select',
          value: t.spec.connectionRef || '',
          options: [{ value: '', label: '— none —' }, ...vc.store.connections.map((c) => ({ value: c.metadata.name, label: c.metadata.name }))],
        },
        { key: 'task', label: 'Task on fire', kind: 'textarea', value: t.spec.task || '' },
      ],
    })
    wires.push({ from: [id, 'fire'], to: ['agent', 'input'] })
  }

  // model (active chat credential)
  if (model) {
    const id = 'model:' + model
    const c = vc.store.credentials.find((x) => x.name === model)
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
          options: [{ value: '', label: '— no model —' }, ...vc.store.credentials.map((x) => ({ value: x.name, label: x.name + (x.model ? ` (${x.model})` : '') }))],
          hint: 'Switch which credential this agent reasons with.',
        },
        {
          key: 'modelFallbacks',
          label: 'Fallbacks',
          kind: 'chips',
          // Saved fallbacks first (preserves their order), then the rest.
          chips: [...fallbacks.filter((n2) => n2 !== model), ...vc.store.credentials.map((x) => x.name).filter((n2) => n2 !== model && !fallbacks.includes(n2))].map((n2) => ({
            value: n2,
            label: n2,
            on: fallbacks.includes(n2),
          })),
          hint: 'If this model fails (outage, rate limit, timeout), try these in order.',
        },
      ],
    })
    wires.push({ from: [id, 'infer'], to: ['agent', 'model'] })
  }

  // Toolsets: only the ones THIS agent links render (scoped to the agent).
  const linked = new Set([...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])])
  const linkedToolsetConns = new Set<string>()
  for (const ts of vc.store.toolsets) {
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
    for (const cn of tconns) if (vc.store.connections.some((c) => c.metadata.name === cn)) wires.push({ from: ['conn:' + cn, 'events'], to: [id, 'tool'] })
  }

  // Only render connections relevant to THIS agent: its direct tools, its linked
  // toolsets' tools, trigger sources, and its notify channel.
  const showConn = new Set<string>([...agentTools, ...linkedToolsetConns])
  trigs.forEach((t) => t.spec.connectionRef && showConn.add(t.spec.connectionRef))
  if (notify) showConn.add(notify)

  for (const c of vc.store.connections) {
    const cn = c.metadata.name
    if (!showConn.has(cn)) continue
    const cat = CONN_CATEGORY[c.spec.type]
    const isChannel = cat === 'channel'
    const isTool = cat === 'tool'
    const id = 'conn:' + cn
    const used = usedConns.has(cn)
    const webish = c.spec.type === 'websearch'
    // A channel talks two ways to the agent, keyed on the SAME notify link:
    // NOTIFY (agent → channel, outbound) and LISTEN (channel → agent, inbound).
    const isDiscordWebhook = c.spec.type === 'discord' && (c.spec.channel || '').startsWith('https://')
    const isDiscordBot = c.spec.type === 'discord' && !isDiscordWebhook
    const canReceive = c.spec.type === 'telegram' || c.spec.type === 'slack' || isDiscordBot
    const isNotify = notify === cn
    const inboundActive = isNotify && canReceive && (isDiscordBot || !!c.status?.webhookPath)
    const inbound = isChannel
      ? !canReceive
        ? { state: 'off' as const, canEnable: false, note: 'Send-only channel — it can notify you, but can’t receive chat.' }
        : !isNotify
          ? { state: 'unlinked' as const, canEnable: false, note: 'Not linked yet — drag this channel onto the agent (or set it as Notify) to route messages here.' }
          : isDiscordBot
            ? { state: 'auto' as const, canEnable: false, note: 'Automatic — the Discord bot delivers messages to this agent while it’s linked.' }
            : c.status?.webhookPath
              ? { state: 'on' as const, canEnable: false, note: 'Receiving — messages from this channel reach the agent.' }
              : { state: 'off' as const, canEnable: true, note: 'Not receiving yet — click Enable to register the inbound webhook.' }
      : undefined
    const outPort = isChannel ? 'listen' : 'events'
    nodes.push({
      id,
      type: isTool ? 'tool' : 'connection',
      title: c.spec.displayName || cn,
      ins: isChannel ? ['notify'] : [],
      outs: [outPort],
      sub: `<span class="mono">${escapeHTML(c.spec.type)}</span>${c.status?.oauthConnected ? ' · connected' : webish ? '' : used ? '' : ' · unwired'}`,
      status: c.status?.oauthConnected || c.status?.phase === 'Ready' || webish ? ['ok', webish ? 'ready' : 'connected'] : ['warn', c.status?.phase || 'setup'],
      canDelete: isTool,
      inbound,
      fields: isTool
        ? [
            { key: 'displayName', label: 'Display name', kind: 'text', value: c.spec.displayName || cn },
            ...(c.spec.type === 'mcp' ? [{ key: 'baseURL', label: 'MCP server URL', kind: 'text' as const, mono: true, value: c.spec.baseURL || '', placeholder: 'https://host/mcp' }] : []),
            { key: 'type', label: 'Type', kind: 'static', value: c.spec.type },
          ]
        : [
            { key: 'type', label: 'Type', kind: 'static', value: c.spec.type },
            ...(c.spec.baseURL ? [{ key: 'baseURL', label: 'Endpoint', kind: 'static' as const, mono: true, value: c.spec.baseURL }] : []),
            { key: 'phase', label: 'Status', kind: 'static', value: c.status?.phase || (webish ? 'ready' : c.status?.oauthConnected ? 'connected' : 'setup') },
          ],
    })
    trigs.forEach((t) => {
      if (t.spec.connectionRef === cn) wires.push({ from: [id, outPort], to: ['trig:' + t.metadata.name, 'src'] })
    })
    if (isNotify && isChannel) wires.push({ from: ['agent', 'result'], to: [id, 'notify'] })
    if (inboundActive) wires.push({ from: [id, 'listen'], to: ['agent', 'input'] })
    if (isTool && agentTools.has(cn)) wires.push({ from: [id, 'events'], to: ['agent', 'tools'] })
  }

  // delegates
  for (const d of a.spec?.delegates || []) {
    const id = 'delegate:' + d
    nodes.push({ id, type: 'delegate', title: d, ins: ['call'], outs: [], sub: 'sub-agent', status: ['off', 'on demand'], canDelete: true })
    wires.push({ from: ['agent', 'delegate'], to: [id, 'call'] })
  }

  return { key: name, nodes, wires, palette: buildPalette(vc, a) }
}

// ---- palette (existing objects grouped by type + "＋ new" per group) --------

function buildPalette(vc: ViewCtx, a: Agent): PaletteGroup[] {
  const name = a.metadata.name
  const agentToolsets = new Set([...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])])
  const agentTools = new Set([...(a.spec?.tools?.interactive?.connections || []), ...(a.spec?.tools?.background?.connections || [])])
  // Tools reachable via a linked toolset count as linked too.
  const toolsetConns = new Set<string>()
  for (const ts of vc.store.toolsets) if (agentToolsets.has(ts.metadata.name)) (ts.spec.connections || []).forEach((c) => toolsetConns.add(c))
  const model = a.spec?.models?.chat
  const notify = a.spec?.defaultNotifyConnection
  const delegates = new Set(a.spec?.delegates || [])

  const newEntry = (key: string, label: string, icon: PaletteEntry['icon']): PaletteEntry => ({ id: 'new:' + key, label, icon })

  const groups: PaletteGroup[] = []

  const schedEntries: PaletteEntry[] = vc.store.schedules.map((s) => ({
    id: 'sched:' + s.metadata.name,
    label: s.metadata.name,
    icon: 'schedule',
    linked: s.spec.agentRef === name,
    sub: s.spec.type === 'wakeup' ? s.spec.runAt : s.spec.schedule,
  }))
  groups.push({ label: 'Schedules', entries: [...schedEntries, newEntry('schedule', 'New', 'schedule')] })

  const trigEntries: PaletteEntry[] = vc.store.triggers.map((t) => ({
    id: 'trig:' + t.metadata.name,
    label: t.metadata.name,
    icon: 'trigger',
    linked: t.spec.agentRef === name,
    sub: t.spec.source,
  }))
  groups.push({ label: 'Triggers', entries: [...trigEntries, newEntry('trigger', 'New', 'trigger')] })

  const toolsetEntries: PaletteEntry[] = vc.store.toolsets.map((t) => ({
    id: 'toolset:' + t.metadata.name,
    label: t.spec.displayName || t.metadata.name,
    icon: 'toolset',
    linked: agentToolsets.has(t.metadata.name),
  }))
  groups.push({ label: 'Toolsets', entries: [...toolsetEntries, newEntry('toolset', 'New', 'toolset')] })

  const toolEntries: PaletteEntry[] = vc.store.connections
    .filter((c) => connCategory(c.spec.type) === 'tool')
    .map((c) => ({
      id: 'conn:' + c.metadata.name,
      label: c.spec.displayName || c.metadata.name,
      icon: 'tool',
      linked: agentTools.has(c.metadata.name) || toolsetConns.has(c.metadata.name),
      sub: c.spec.type,
    }))
  groups.push({
    label: 'Tools',
    entries: [...toolEntries, newEntry('tool-mcp', 'MCP', 'tool'), newEntry('tool-github', 'GitHub', 'tool'), newEntry('tool-web', 'Web', 'tool')],
  })

  const chanEntries: PaletteEntry[] = vc.store.connections
    .filter((c) => connCategory(c.spec.type) === 'channel')
    .map((c) => ({
      id: 'conn:' + c.metadata.name,
      label: c.spec.displayName || c.metadata.name,
      icon: 'connection',
      linked: notify === c.metadata.name,
      sub: c.spec.type,
    }))
  groups.push({ label: 'Channels', entries: [...chanEntries, newEntry('connection', 'New', 'connection')] })

  const modelEntries: PaletteEntry[] = vc.store.credentials.map((c) => ({
    id: 'model:' + c.name,
    label: c.name,
    icon: 'model',
    linked: model === c.name,
    sub: c.model,
  }))
  groups.push({ label: 'Models', entries: [...modelEntries, newEntry('model', 'New', 'model')] })

  const delegateEntries: PaletteEntry[] = vc.store.agents
    .filter((x) => x.metadata.name !== name)
    .map((x) => ({
      id: 'delegate:' + x.metadata.name,
      label: x.spec?.displayName || x.metadata.name,
      icon: 'delegate',
      linked: delegates.has(x.metadata.name),
    }))
  if (delegateEntries.length) groups.push({ label: 'Delegates', entries: delegateEntries })

  return groups
}

// ---- drag existing object → reference to this agent ------------------------

async function flowAddExisting(id: string): Promise<string | null> {
  if (!cur) return null
  const { vc, agent } = cur
  try {
    if (id.startsWith('sched:')) {
      const n = id.slice(6)
      const s = vc.store.schedules.find((x) => x.metadata.name === n)
      if (s && s.spec.agentRef && s.spec.agentRef !== agent && !(await confirmModal({ title: `Reassign schedule “${n}”?`, message: `It currently runs as ${s.spec.agentRef}. Reassign it to ${agent}?`, confirmLabel: 'Reassign' }))) return null
      await updateSchedule(vc, n, { agentRef: agent }, 'Schedule assigned.')
      return id
    }
    if (id.startsWith('trig:')) {
      const n = id.slice(5)
      const t = vc.store.triggers.find((x) => x.metadata.name === n)
      if (t && t.spec.agentRef && t.spec.agentRef !== agent && !(await confirmModal({ title: `Reassign trigger “${n}”?`, message: `It currently fires ${t.spec.agentRef}. Reassign it to ${agent}?`, confirmLabel: 'Reassign' }))) return null
      await updateTrigger(vc, n, { agentRef: agent }, 'Trigger assigned.')
      return id
    }
    if (id.startsWith('toolset:')) {
      await linkToolset(vc, agent, id.slice(8))
      vc.notify('Toolset linked.')
      return id
    }
    if (id.startsWith('model:')) {
      await updateAgent(vc, agent, { modelCredential: id.slice(6) }, 'Model assigned.')
      return id
    }
    if (id.startsWith('delegate:')) {
      const n = id.slice(9)
      const a = vc.store.agent(agent)
      const cur2 = a?.spec?.delegates || []
      if (!cur2.includes(n)) await updateAgent(vc, agent, { delegates: [...cur2, n] }, 'Delegate added.')
      return id
    }
    if (id.startsWith('conn:')) {
      const cn = id.slice(5)
      const c = vc.store.connections.find((x) => x.metadata.name === cn)
      const cat = c ? connCategory(c.spec.type) : 'connection'
      if (cat === 'channel') await updateAgent(vc, agent, { notifyConnection: cn }, 'Notify channel set.')
      else await wireToolTo(vc, agent, 'agent', cn)
      return id
    }
  } catch (e) {
    flow?.toast('Failed: ' + (e as Error).message)
    return null
  }
  return null
}

// ---- draft create (＋ new palette entries) ---------------------------------

function flowDraftFor(key: string): DraftSpec | null {
  if (!cur) return null
  const { vc, agent } = cur
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
        { key: 'source', label: 'Source', kind: 'select', value: 'webhook', options: ['webhook', 'github'].map((v) => ({ value: v, label: v })) },
        { key: 'connectionRef', label: 'Connection', kind: 'select', value: '', options: [{ value: '', label: '— none —' }, ...vc.store.connections.map((c) => ({ value: c.metadata.name, label: c.metadata.name }))] },
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
  // Tool nodes: pick an existing tool of this type or create a new one — either
  // way it wires to the chosen target (this agent, or one of its toolsets).
  const toolKinds: Record<string, { type: string; label: string; mcp?: boolean }> = {
    'tool-mcp': { type: 'mcp', label: 'MCP', mcp: true },
    'tool-github': { type: 'github', label: 'GitHub' },
    'tool-web': { type: 'websearch', label: 'Web search' },
  }
  if (key in toolKinds) {
    const t = toolKinds[key]
    const a = vc.store.agent(agent)
    const existing = vc.store.connections.filter((c) => c.spec.type === t.type)
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
          options: [{ value: '', label: `+ Create a new ${t.label} tool` }, ...existing.map((c) => ({ value: c.metadata.name, label: c.spec.displayName || c.metadata.name }))],
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
    const a = vc.store.agent(agent)
    const mine = new Set([...(a?.spec?.tools?.interactive?.toolsets || []), ...(a?.spec?.tools?.background?.toolsets || [])])
    const unlinked = vc.store.toolsets.filter((t) => !mine.has(t.metadata.name))
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
          options: [{ value: '', label: '+ Create a new toolset' }, ...unlinked.map((t) => ({ value: t.metadata.name, label: t.spec.displayName || t.metadata.name }))],
          hint: unlinked.length ? 'link a shared one, or create a new bundle below' : undefined,
        },
        { key: 'name', label: 'New toolset name', kind: 'text', placeholder: 'dev-tools', hint: 'only when creating a new toolset' },
        { key: 'displayName', label: 'Display name', kind: 'text', placeholder: 'optional' },
      ],
    }
  }
  return null
}

// Write the object a draft describes; return its real flow-node id on success.
async function flowCreate(key: string, values: Record<string, string | string[]>): Promise<string | null> {
  if (!cur) return null
  const { vc, agent } = cur
  const s = (k: string): string => String(values[k] ?? '').trim()
  const name = s('name')
  if (!key.startsWith('tool-') && !/^[a-z0-9-]+$/.test(name)) {
    flow?.toast('Name must be lowercase letters, numbers and dashes')
    return null
  }
  try {
    if (key === 'schedule') {
      await vc.api.send('POST', '/api/schedules', { name, agentRef: agent, type: 'cron', schedule: s('schedule'), timeZone: s('timeZone'), task: s('task') })
      await vc.store.loadSchedules()
      return 'sched:' + name
    }
    if (key === 'trigger') {
      await vc.api.send('POST', '/api/triggers', { name, agentRef: agent, source: s('source') || 'webhook', connectionRef: s('connectionRef'), task: s('task') })
      await vc.store.loadTriggers()
      return 'trig:' + name
    }
    if (key === 'model') {
      await vc.api.send('POST', '/api/credentials', { name, provider: s('provider'), baseURL: s('baseURL'), model: s('model'), apiKey: s('apiKey') })
      await vc.api.send('PUT', `/api/agents/${encodeURIComponent(agent)}`, { modelCredential: name })
      await vc.store.loadCredentials()
      await vc.store.loadAgents()
      return 'model:' + name
    }
    if (key === 'connection') {
      await vc.api.send('POST', '/api/connections', { name, type: s('type'), displayName: s('displayName') })
      await vc.store.loadConnections()
      return 'conn:' + name
    }
    const toolType: Record<string, string> = { 'tool-mcp': 'mcp', 'tool-github': 'github', 'tool-web': 'websearch' }
    if (key in toolType) {
      let cn = s('existing')
      if (!cn) {
        if (!/^[a-z0-9-]+$/.test(name)) {
          flow?.toast('Pick an existing tool, or give the new one a name (a-z, 0-9, dashes)')
          return null
        }
        await vc.api.send('POST', '/api/connections', { name, type: toolType[key], baseURL: s('baseURL') })
        await vc.store.loadConnections()
        cn = name
      }
      await wireToolTo(vc, agent, s('target') || 'agent', cn)
      return 'conn:' + cn
    }
    if (key === 'toolset') {
      let tn = s('existing')
      if (!tn) {
        if (!/^[a-z0-9-]+$/.test(name)) {
          flow?.toast('Pick a toolset, or give the new one a name (a-z, 0-9, dashes)')
          return null
        }
        await vc.api.send('POST', '/api/toolsets', { name, displayName: s('displayName'), families: ['core'] })
        await vc.store.loadToolsets()
        tn = name
      }
      await linkToolset(vc, agent, tn)
      return 'toolset:' + tn
    }
  } catch (e) {
    flow?.toast('Create failed: ' + (e as Error).message)
    return null
  }
  return null
}

// ---- cable gesture → spec mutation -----------------------------------------

async function flowLink(from: [string, string], to: [string, string]): Promise<void> {
  if (!cur) return
  const { vc, agent } = cur
  const [fromNode] = from
  const [toNode, toPort] = to
  // connection.events → trigger.src : point the trigger at this connection
  if (fromNode.startsWith('conn:') && toNode.startsWith('trig:') && toPort === 'src') {
    return void updateTrigger(vc, toNode.slice(5), { connectionRef: fromNode.slice(5) }, 'Trigger connected.')
  }
  // model.infer → agent.model : switch the agent's model credential
  if (fromNode.startsWith('model:') && toNode === 'agent' && toPort === 'model') {
    return void updateAgent(vc, agent, { modelCredential: fromNode.slice(6) }, 'Model reassigned.')
  }
  // agent.result → connection.notify : set the default notify channel
  if (fromNode === 'agent' && toNode.startsWith('conn:') && toPort === 'notify') {
    return void updateAgent(vc, agent, { notifyConnection: toNode.slice(5) }, 'Notify channel set.')
  }
  // connection.listen → agent.input : link a channel inbound. The notify link is
  // symmetric — it also routes messages FROM the channel to the agent.
  if (fromNode.startsWith('conn:') && toNode === 'agent' && toPort === 'input') {
    return void updateAgent(vc, agent, { notifyConnection: fromNode.slice(5) }, 'Channel linked — it can message the agent both ways.')
  }
  // toolset.use → agent.tools : link the shared toolset to this agent
  if (fromNode.startsWith('toolset:') && toNode === 'agent' && toPort === 'tools') {
    await linkToolset(vc, agent, fromNode.slice(8))
    vc.notify('Toolset linked.')
    return
  }
  // tool.events → toolset.tool : add the tool to the bundle (derived families)
  if (fromNode.startsWith('conn:') && toNode.startsWith('toolset:') && toPort === 'tool') {
    const tsName = toNode.slice(8)
    const cn = fromNode.slice(5)
    const ts = vc.store.toolsets.find((x) => x.metadata.name === tsName)
    const conns = ts?.spec.connections || []
    if (conns.includes(cn)) return
    const next = [...conns, cn]
    return void updateToolset(vc, tsName, { connections: next, families: vc.store.familiesFor(next) })
  }
  // tool.events → agent.tools : give the agent this tool directly (its own grant)
  if (fromNode.startsWith('conn:') && toNode === 'agent' && toPort === 'tools') {
    const cn = fromNode.slice(5)
    const a = vc.store.agent(agent)
    const cur2 = a?.spec?.tools?.interactive?.connections || []
    if (cur2.includes(cn)) return
    const next = [...cur2, cn]
    return void updateAgent(vc, agent, { interactiveConnections: next, interactiveFamilies: vc.store.familiesFor(next) }, 'Tool added to agent.')
  }
  flow?.toast('These ports don’t connect — try tool → toolset or agent, toolset → agent, or schedule/trigger → agent.')
}

async function flowEdit(id: string, values: Record<string, string | string[]>): Promise<void> {
  if (!cur) return
  const { vc, agent } = cur
  const str = (v: string | string[]): string => (Array.isArray(v) ? v.join(',') : v)
  const patch: Record<string, unknown> = {}
  const take = (k: string, as = k): void => {
    if (k in values) patch[as] = str(values[k])
  }
  if (id === 'agent') {
    take('displayName')
    take('systemPrompt')
    take('autonomy')
    if (Object.keys(patch).length) await updateAgent(vc, agent, patch)
  } else if (id.startsWith('sched:')) {
    take('schedule')
    take('timeZone')
    take('task')
    if (Object.keys(patch).length) await updateSchedule(vc, id.slice(6), patch)
  } else if (id.startsWith('trig:')) {
    take('source')
    take('connectionRef')
    take('task')
    if (Object.keys(patch).length) await updateTrigger(vc, id.slice(5), patch)
  } else if (id.startsWith('model:')) {
    const p: Record<string, unknown> = {}
    if ('modelCredential' in values) p.modelCredential = str(values.modelCredential)
    if ('modelFallbacks' in values) p.modelFallbacks = Array.isArray(values.modelFallbacks) ? values.modelFallbacks : []
    if (Object.keys(p).length) await updateAgent(vc, agent, p, 'Model updated.')
  } else if (id.startsWith('conn:')) {
    take('displayName')
    take('baseURL')
    if (Object.keys(patch).length) await updateConnection(vc, id.slice(5), patch)
  } else if (id.startsWith('toolset:')) {
    if ('displayName' in values) await updateToolset(vc, id.slice(8), { displayName: str(values.displayName) })
  }
}

// Turn on inbound chat for a channel node: registers the webhook (telegram/slack)
// so the channel the agent notifies can also talk back to it.
async function flowEnableInbound(id: string): Promise<void> {
  if (!cur || !id.startsWith('conn:')) return
  await enableInbound(cur.vc, id.slice(5))
}

async function flowRun(id: string): Promise<void> {
  if (!cur) return
  const { vc } = cur
  if (id.startsWith('sched:')) await runSchedule(vc, id.slice(6))
  else if (id.startsWith('trig:')) await runTrigger(vc, id.slice(5))
}

async function flowDelete(id: string): Promise<void> {
  if (!cur) return
  const { vc, agent } = cur
  if (id.startsWith('sched:')) {
    if (await confirmModal({ title: `Delete schedule “${id.slice(6)}”?`, danger: true, confirmLabel: 'Delete' })) await deleteSchedule(vc, id.slice(6))
  } else if (id.startsWith('trig:')) {
    if (await confirmModal({ title: `Delete trigger “${id.slice(5)}”?`, danger: true, confirmLabel: 'Delete' })) await deleteTrigger(vc, id.slice(5))
  } else if (id.startsWith('delegate:')) {
    const a = vc.store.agent(agent)
    const next = (a?.spec?.delegates || []).filter((d) => d !== id.slice(9))
    await updateAgent(vc, agent, { delegates: next }, 'Delegate removed.')
  } else if (id.startsWith('toolset:')) {
    // Unlink the shared toolset from this agent; the object stays for others.
    const ts = id.slice(8)
    const a = vc.store.agent(agent)
    const inter = (a?.spec?.tools?.interactive?.toolsets || []).filter((t) => t !== ts)
    const bg = (a?.spec?.tools?.background?.toolsets || []).filter((t) => t !== ts)
    await updateAgent(vc, agent, { interactiveToolsets: inter, backgroundToolsets: bg }, 'Toolset unlinked.')
  } else if (id.startsWith('conn:')) {
    // Remove a Tool from the agent's own grant. Stays for others; if it's only
    // here via a toolset/trigger, say so.
    const cn = id.slice(5)
    const a = vc.store.agent(agent)
    const inter = a?.spec?.tools?.interactive?.connections || []
    const bg = a?.spec?.tools?.background?.connections || []
    if (inter.includes(cn) || bg.includes(cn)) {
      const ni = inter.filter((x) => x !== cn)
      const nb = bg.filter((x) => x !== cn)
      await updateAgent(vc, agent, { interactiveConnections: ni, backgroundConnections: nb, interactiveFamilies: vc.store.familiesFor(ni), backgroundFamilies: vc.store.familiesFor(nb) }, 'Tool removed.')
    } else {
      flow?.toast('This tool comes from a linked toolset or a trigger — remove it there.')
    }
  }
}
