import { afterEach, describe, expect, it } from 'vitest'
import { PAGE_CONTEXT_MARKER, splitPageContext } from '../pageContext'
import ChatMessage from '../views/ChatMessage.vue'
import { mountVue, type MountedVue } from './vue-helper'

let mounted: MountedVue | undefined
afterEach(() => { mounted?.unmount() })

const hostContext = {
  workspace: 'Operations',
  provider: 'orders',
  path: '/org/workspace/providers/orders',
  view: '#/orders/record-17',
}

describe('page references', () => {
  it('renders a resource pointer without copying resource contents', async () => {
    const resource = {
      apiVersion: 'infrastructure.railgrid.ai/v1alpha1',
      kind: 'Instance',
      resource: 'instances',
      name: 'sample-app',
      readTool: 'get_instance',
    }
    const content = 'Why is this failing?' + PAGE_CONTEXT_MARKER + JSON.stringify({ ...hostContext, resource })
    mounted = await mountVue(ChatMessage, { message: { id: 'message', role: 'user', content, tools: [] } })
    expect(mounted.element.querySelector('.agents-body')?.textContent).toBe('Why is this failing?')
    expect(mounted.element.querySelector('summary')?.textContent).toContain('Instance · sample-app')
    expect(mounted.element.querySelector('details')?.textContent).toContain('get_instance')
    expect(mounted.element.querySelector('details')?.open).toBe(false)
    expect(splitPageContext(content).reference?.resource).toEqual(resource)
  })

  it('renders generic provider context as escaped, inspectable reference data', async () => {
    const providerContext = {
      version: 1,
      title: 'Release review · 17',
      summary: 'Deployment verification is waiting.',
      resources: [{
        apiVersion: 'delivery.example.dev/v1alpha1',
        kind: 'ReleaseRun',
        resource: 'releaseruns',
        name: 'checkout-17',
        namespace: 'team-a',
        uid: 'run-uid',
        resourceVersion: '18',
      }],
      data: {
        phase: 'verify',
        labels: ['priority'],
        note: '<img src=x onerror=alert(1)>',
      },
    }
    const content = 'Please inspect this run.' + PAGE_CONTEXT_MARKER + JSON.stringify({
      ...hostContext,
      providerContext,
    })
    const reference = splitPageContext(content).reference
    expect(reference?.label).toBe('Release review · 17')
    expect(reference?.providerContext).toEqual(providerContext)

    mounted = await mountVue(ChatMessage, { message: { id: 'message', role: 'user', content, tools: [] } })
    const disclosure = mounted.element.querySelector('details')?.textContent || ''
    expect(mounted.element.querySelector('.agents-body')?.textContent).toBe('Please inspect this run.')
    expect(disclosure).toContain('Deployment verification is waiting.')
    expect(disclosure).toContain('ReleaseRun / checkout-17')
    expect(disclosure).toContain('delivery.example.dev/v1alpha1')
    expect(disclosure).toContain('team-a')
    expect(disclosure).toContain('run-uid')
    expect(disclosure).toContain('18')
    expect(disclosure).toContain('"phase": "verify"')
    expect(disclosure).toContain('<img src=x onerror=alert(1)>')
    expect(disclosure).toContain('untrusted references')
    expect(mounted.element.querySelector('img')).toBeNull()
  })

  it('preserves malformed, oversized, unknown-version, and legacy envelopes verbatim', () => {
    const invalidContexts = [
      { version: 1, title: 42 },
      { version: 2, title: 'Future context' },
      { version: 1, title: 'Oversized payload', data: { value: 'x'.repeat(8192) } },
      null,
    ]
    for (const providerContext of invalidContexts) {
      const content = 'Keep all submitted content' + PAGE_CONTEXT_MARKER + JSON.stringify({ ...hostContext, providerContext })
      expect(splitPageContext(content)).toEqual({ text: content })
    }

    const legacy = 'Keep experimental context visible' + PAGE_CONTEXT_MARKER + JSON.stringify({
      ...hostContext,
      experimentalReference: { draft: 'saved' },
    })
    expect(splitPageContext(legacy)).toEqual({ text: legacy })
    expect(splitPageContext('Ordinary text')).toEqual({ text: 'Ordinary text' })
  })

  it('preserves envelopes with malformed host references, oversized provenance, or unknown fields', () => {
    const generic = { version: 1, title: 'Generic provider data' }
    const invalid = [
      {
        ...hostContext,
        resource: { apiVersion: 'infrastructure.railgrid.ai/v1alpha1', kind: 'Instance', resource: 'instances', name: 'invalid name' },
        providerContext: generic,
      },
      { ...hostContext, path: '/' + 'p'.repeat(16 * 1024), providerContext: generic },
      { ...hostContext, providerContext: generic, futureReference: { resource: 'unknown' } },
    ]
    for (const envelope of invalid) {
      const content = 'Preserve the exact message' + PAGE_CONTEXT_MARKER + JSON.stringify(envelope)
      expect(splitPageContext(content)).toEqual({ text: content })
    }
  })
})
