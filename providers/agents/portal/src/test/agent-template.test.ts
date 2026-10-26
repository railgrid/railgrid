// Parity between the two instantiators. internal/gentemplates renders, for
// every built-in template, the Go instantiator's exact output for a fixed set
// of inputs; this test feeds the same inputs to agent-template.ts and compares.
// A template or a rule changed on one side only fails here, by name.

import { describe, expect, it } from 'vitest'
import catalog from '../../public/agent-templates.json'
import {
  instantiateTemplate,
  parameterValues,
  previewPrompt,
  templateBackend,
  TemplateInputError,
  type AgentTemplate,
  type TemplateInputs,
} from '../agent-template'

interface Fixture {
  template: string
  inputs: TemplateInputs
  agent: { metadata: { name: string }; spec: Record<string, unknown> }
  schedules: { metadata: { name: string }; spec: Record<string, unknown> }[] | null
}

const fixtures = Object.values(
  import.meta.glob<{ default: Fixture }>('./fixtures/agent-templates/*.json', { eager: true }),
).map((m) => m.default)
const templates = catalog as AgentTemplate[]
const byName = (name: string): AgentTemplate => {
  const found = templates.find((t) => t.metadata.name === name)
  if (!found) throw new Error(`template ${name} not in agent-templates.json`)
  return found
}

// prune drops the shapes Go's encoder and this file disagree on without
// meaning anything: undefined/null, empty strings, empty arrays and empty
// objects (a Go struct with every field omitempty marshals as {}).
function prune(value: unknown): unknown {
  if (Array.isArray(value)) {
    const items = value.map(prune).filter((v) => v !== undefined)
    return items.length ? items : undefined
  }
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const p = prune(v)
      if (p !== undefined) out[k] = p
    }
    return Object.keys(out).length ? out : undefined
  }
  if (value === null || value === undefined || value === '') return undefined
  return value
}

describe('agent template instantiation parity', () => {
  it('has a fixture for every template and a template for every fixture', () => {
    expect(fixtures.length).toBeGreaterThan(0)
    expect(fixtures.map((f) => f.template).sort()).toEqual(templates.map((t) => t.metadata.name).sort())
  })

  for (const fixture of fixtures) {
    it(`reproduces the Go instantiator for ${fixture.template}`, () => {
      const { agent, schedules } = instantiateTemplate(byName(fixture.template), fixture.inputs)
      expect(agent.metadata.name).toBe(fixture.agent.metadata.name)
      expect(prune(agent.spec)).toEqual(prune(fixture.agent.spec))
      expect(schedules.map((s) => s.metadata.name)).toEqual((fixture.schedules ?? []).map((s) => s.metadata.name))
      expect(prune(schedules.map((s) => s.spec))).toEqual(prune((fixture.schedules ?? []).map((s) => s.spec)))
    })
  }
})

describe('agent template inputs', () => {
  it('renders list parameters as bullets and applies defaults', () => {
    const reporter = byName('news-reporter')
    const { agent, schedules } = instantiateTemplate(reporter, {
      name: 'daily',
      parameters: { topics: ' kcp \n\nkubernetes\n' },
      wiring: { modelCredential: 'openai', channel: 'tg' },
    })
    expect(agent.spec.systemPrompt).toContain('- kcp\n- kubernetes\n')
    expect(agent.spec.displayName).toBe('News reporter')
    expect(agent.spec.backend).toEqual({ type: 'model', model: { credentials: { chat: 'openai' } } })
    expect(agent.spec.channels).toEqual([{ name: 'primary', connectionRef: 'tg', primary: true }])
    expect(schedules).toHaveLength(1)
    expect(schedules[0].metadata.name).toBe('daily-daily-digest')
    expect(schedules[0].spec).toMatchObject({ agentRef: 'daily', type: 'cron', schedule: '0 8 * * 1-5', channelRef: 'primary' })
  })

  it('wires a harness template and defaults its working directory', () => {
    const reviewer = byName('code-reviewer')
    expect(templateBackend(reviewer)).toBe('harness')
    const { agent } = instantiateTemplate(reviewer, {
      name: 'rev',
      displayName: 'PR reviewer',
      parameters: { repositories: 'railgrid/railgrid' },
      wiring: { edge: 'MacOSServer/mini', harnessCredential: 'claude', githubConnection: 'gh' },
    })
    expect(agent.spec.displayName).toBe('PR reviewer')
    expect(agent.spec.backend).toEqual({
      type: 'harness',
      harness: { workspace: 'persistent', edgeRef: { kind: 'MacOSServer', name: 'mini' }, credentialRef: 'claude', githubConnectionRef: 'gh' },
    })
    // The catalog JSON carries Go's zero-valued tools block; a harness agent must
    // still grant nothing.
    expect(agent.spec.tools?.interactive?.families ?? []).toEqual([])
    expect(agent.spec.tools?.background?.families ?? []).toEqual([])
  })

  it('reports every input problem at once', () => {
    const reporter = byName('news-reporter')
    let caught: unknown
    try {
      instantiateTemplate(reporter, { name: 'Bad Name', parameters: { nope: 'x' }, wiring: { edge: 'LinuxServer/x' } })
    } catch (e) {
      caught = e
    }
    expect(caught).toBeInstanceOf(TemplateInputError)
    const problems = (caught as TemplateInputError).problems
    expect(problems.some((p) => p.includes('name "Bad Name"'))).toBe(true)
    expect(problems.some((p) => p.includes('parameter "topics"'))).toBe(true)
    expect(problems.some((p) => p.includes('parameter "nope" is not declared'))).toBe(true)
    expect(problems.some((p) => p.includes('wiring "modelCredential"'))).toBe(true)
    expect(problems.some((p) => p.includes('wiring "edge" is not declared'))).toBe(true)
  })

  it('refuses an edge that is not <Kind>/<name> on a host kind', () => {
    const reviewer = byName('code-reviewer')
    for (const edge of ['box', 'KubernetesCluster/prod', 'LinuxServer/']) {
      expect(() => instantiateTemplate(reviewer, {
        name: 'r', parameters: { repositories: 'a/b' },
        wiring: { edge, harnessCredential: 'c', githubConnection: 'g' },
      })).toThrow(TemplateInputError)
    }
  })

  it('previews the prompt with what it has and names what is missing', () => {
    const reporter = byName('news-reporter')
    expect(parameterValues(reporter).missing.map((p) => p.key)).toEqual(['topics'])
    const preview = previewPrompt(reporter, { topics: 'kcp' })
    expect(preview).toContain('- kcp')
    expect(preview).not.toContain('{{')
  })
})
