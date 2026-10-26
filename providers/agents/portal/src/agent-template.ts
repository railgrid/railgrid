// Agent templates: curated starting points instantiated into an ordinary Agent
// (plus the Schedules it needs) by wiring in what the template cannot know —
// which model credential, channel, connection or machine.
//
// This is the TypeScript twin of providers/agents/templates (Go). The catalog
// ships in the bundle as /agent-templates.json, rendered from the same YAML the
// provider embeds, and internal/gentemplates also writes one fixture per
// template under test/fixtures/agent-templates/ holding the Go instantiator's
// exact output; agent-template.test.ts asserts this file reproduces it. Change
// one side and the parity test says which.

import {
  AGENT_BACKEND_HARNESS,
  AGENT_BACKEND_MODEL,
  HARNESS_EDGE_KINDS,
  type Agent,
  type AgentBackendType,
  type HarnessEdgeKind,
  type Schedule,
} from './types'

export type TemplateParameterType = 'string' | 'text' | 'list'

export interface TemplateParameter {
  key: string
  label: string
  type: TemplateParameterType
  required?: boolean
  default?: string
  description?: string
}

/** Wiring slot kinds, each the Agent field it fills (see templates/types.go). */
export type TemplateSlotKind =
  | 'modelCredential'
  | 'channel'
  | 'connection'
  | 'edge'
  | 'harnessCredential'
  | 'githubConnection'

export interface TemplateWiringSlot {
  key: string
  slot: TemplateSlotKind
  connectionType?: string
  required?: boolean
  description?: string
}

export interface TemplateSchedule {
  name: string
  spec: Omit<Schedule['spec'], 'agentRef'> & { agentRef?: string }
}

export interface AgentTemplate {
  apiVersion: string
  kind: string
  metadata: { name: string }
  spec: {
    displayName: string
    description?: string
    category: string
    suggestedName?: string
    parameters?: TemplateParameter[]
    wiring?: TemplateWiringSlot[]
    agent: Agent['spec']
    schedules?: TemplateSchedule[]
  }
}

export interface TemplateInputs {
  name: string
  displayName?: string
  /** Values by parameter key; a list parameter is one item per line. */
  parameters?: Record<string, string>
  /** Workspace object names by slot key; an edge slot is "<Kind>/<name>". */
  wiring?: Record<string, string>
}

export interface TemplateInstance {
  agent: Agent
  schedules: Schedule[]
}

/** A problem with the caller's inputs, as opposed to a malformed template. */
export class TemplateInputError extends Error {
  readonly problems: string[]
  constructor(problems: string[]) {
    super(problems.join('; '))
    this.name = 'TemplateInputError'
    this.problems = problems
  }
}

export const TEMPLATE_CATEGORIES = ['engineering', 'research', 'operations', 'assistant'] as const

const PLACEHOLDER_RE = /\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}/g
const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/
const PRIMARY_CHANNEL = 'primary'

/** templateBackend is the backend a template's agent runs on. */
export function templateBackend(tmpl: AgentTemplate): AgentBackendType {
  return tmpl.spec.agent.backend?.type === AGENT_BACKEND_HARNESS ? AGENT_BACKEND_HARNESS : AGENT_BACKEND_MODEL
}

/** listItems splits a list parameter's raw value: one per line, trimmed, blanks dropped. */
export function listItems(raw: string): string[] {
  return raw.split('\n').map((line) => line.trim()).filter(Boolean)
}

function renderList(raw: string): string {
  return listItems(raw).map((item) => `- ${item}`).join('\n')
}

function render(text: string, values: Record<string, string>): string {
  return text.replace(PLACEHOLDER_RE, (match, key: string) => (key in values ? values[key] : match))
}

/**
 * parameterValues resolves what each placeholder renders to: the input, else
 * the default, lists as bullets. Missing required values are reported, not
 * thrown, so a live preview can render what it has.
 */
export function parameterValues(
  tmpl: AgentTemplate,
  parameters: Record<string, string> = {},
): { values: Record<string, string>; missing: TemplateParameter[] } {
  const values: Record<string, string> = {}
  const missing: TemplateParameter[] = []
  for (const p of tmpl.spec.parameters ?? []) {
    let raw = (parameters[p.key] ?? '').trim()
    if (!raw) raw = p.default ?? ''
    if (p.type === 'list' && listItems(raw).length === 0) raw = ''
    if (!raw && p.required) {
      missing.push(p)
      continue
    }
    values[p.key] = p.type === 'list' ? renderList(raw) : raw
  }
  return { values, missing }
}

/** previewPrompt renders the system prompt with whatever parameters are known. */
export function previewPrompt(tmpl: AgentTemplate, parameters: Record<string, string> = {}): string {
  return render(tmpl.spec.agent.systemPrompt ?? '', parameterValues(tmpl, parameters).values)
}

/** templateFamilies lists the tool families a template grants interactively. */
export function templateFamilies(tmpl: AgentTemplate): string[] {
  return (tmpl.spec.agent.tools?.interactive?.families ?? []).filter((f) => f !== 'core')
}

/** slotsOfKind lists a template's wiring slots of one kind. */
export function slotsOfKind(tmpl: AgentTemplate, kind: TemplateSlotKind): TemplateWiringSlot[] {
  return (tmpl.spec.wiring ?? []).filter((w) => w.slot === kind)
}

/**
 * instantiateTemplate renders a template into the Agent and Schedules to
 * create. Pure, exactly like templates.Instantiate: whether a wired name
 * exists in the workspace is the caller's business.
 */
export function instantiateTemplate(tmpl: AgentTemplate, inputs: TemplateInputs): TemplateInstance {
  const problems: string[] = []
  const name = (inputs.name ?? '').trim()
  if (!name) problems.push('name is required')
  else if (!NAME_RE.test(name)) problems.push(`name "${name}" must be lowercase letters, digits and dashes`)

  const { values, missing } = parameterValues(tmpl, inputs.parameters)
  for (const p of missing) problems.push(`parameter "${p.key}" (${p.label}) is required`)
  const declaredParams = new Set((tmpl.spec.parameters ?? []).map((p) => p.key))
  for (const key of Object.keys(inputs.parameters ?? {})) {
    if (!declaredParams.has(key)) problems.push(`parameter "${key}" is not declared by template "${tmpl.metadata.name}"`)
  }

  const wired: Record<string, string> = {}
  const slots = tmpl.spec.wiring ?? []
  for (const w of slots) {
    const v = (inputs.wiring?.[w.key] ?? '').trim()
    if (!v) {
      if (w.required) problems.push(`wiring "${w.key}" (${w.slot}) is required`)
      continue
    }
    wired[w.key] = v
  }
  const declaredSlots = new Set(slots.map((w) => w.key))
  for (const key of Object.keys(inputs.wiring ?? {})) {
    if (!declaredSlots.has(key)) problems.push(`wiring "${key}" is not declared by template "${tmpl.metadata.name}"`)
  }
  if (problems.length) throw new TemplateInputError(problems)

  const spec: Agent['spec'] = clone(tmpl.spec.agent)
  spec.displayName = (inputs.displayName ?? '').trim() || tmpl.spec.displayName
  if (!(spec.description ?? '').trim()) spec.description = tmpl.spec.description
  if (spec.description !== undefined) spec.description = render(spec.description, values)
  if (spec.systemPrompt !== undefined) spec.systemPrompt = render(spec.systemPrompt, values)

  const harness = templateBackend(tmpl) === AGENT_BACKEND_HARNESS
  const harnessBlock = () => {
    spec.backend = spec.backend ?? {}
    spec.backend.harness = spec.backend.harness ?? { edgeRef: { kind: 'LinuxServer', name: '' }, credentialRef: '' }
    return spec.backend.harness
  }
  for (const w of slots) {
    const v = wired[w.key]
    if (v === undefined) continue
    switch (w.slot) {
      case 'modelCredential': {
        spec.backend = spec.backend ?? {}
        spec.backend.model = spec.backend.model ?? {}
        spec.backend.model.credentials = { ...(spec.backend.model.credentials ?? {}), chat: v }
        break
      }
      case 'channel':
        spec.channels = [{ name: PRIMARY_CHANNEL, connectionRef: v, primary: true }]
        break
      case 'connection': {
        spec.tools = spec.tools ?? {}
        spec.tools.interactive = spec.tools.interactive ?? {}
        spec.tools.background = spec.tools.background ?? {}
        spec.tools.interactive.connections = appendUnique(spec.tools.interactive.connections ?? [], v)
        spec.tools.background.connections = appendUnique(spec.tools.background.connections ?? [], v)
        break
      }
      case 'edge': {
        const slash = v.indexOf('/')
        const kind = slash > 0 ? v.slice(0, slash) : ''
        const edgeName = slash > 0 ? v.slice(slash + 1) : ''
        if (!kind || !edgeName) throw new TemplateInputError([`wiring "${w.key}" must be "<Kind>/<name>" (LinuxServer/dev-box), got "${v}"`])
        if (!HARNESS_EDGE_KINDS.includes(kind as HarnessEdgeKind)) {
          throw new TemplateInputError([`wiring "${w.key}" names edge kind "${kind}"; a harness runs on a LinuxServer or MacOSServer`])
        }
        harnessBlock().edgeRef = { kind: kind as HarnessEdgeKind, name: edgeName }
        break
      }
      case 'harnessCredential':
        harnessBlock().credentialRef = v
        break
      case 'githubConnection':
        harnessBlock().githubConnectionRef = v
        break
    }
  }
  spec.backend = spec.backend ?? {}
  if (harness) {
    spec.backend.type = AGENT_BACKEND_HARNESS
    const h = harnessBlock()
    if (!h.workspace) h.workspace = 'persistent'
  } else {
    spec.backend.type = AGENT_BACKEND_MODEL
  }

  const agent: Agent = { metadata: { name }, spec }
  const schedules: Schedule[] = (tmpl.spec.schedules ?? []).map((s) => {
    const sched = clone(s.spec) as Schedule['spec']
    sched.agentRef = name
    if (sched.schedule !== undefined) sched.schedule = render(sched.schedule, values).trim()
    if (sched.timeZone !== undefined) sched.timeZone = render(sched.timeZone, values)
    if (sched.task !== undefined) sched.task = render(sched.task, values)
    if (sched.checklist !== undefined) sched.checklist = render(sched.checklist, values)
    return { metadata: { name: `${name}-${s.name}` }, spec: sched }
  })
  return { agent, schedules }
}

// clone is a JSON round-trip rather than structuredClone: a template handed in
// from a Vue ref is a reactive proxy, which structuredClone refuses, and the
// catalog is plain JSON to begin with.
function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function appendUnique(list: string[], v: string): string[] {
  return list.includes(v) ? list : [...list, v]
}
