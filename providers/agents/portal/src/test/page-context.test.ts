import { afterEach, describe, expect, it } from 'vitest'
import { PAGE_CONTEXT_MARKER, splitPageContext } from '../pageContext'
import ChatMessage from '../views/ChatMessage.vue'
import { mountVue, type MountedVue } from './vue-helper'
let mounted: MountedVue | undefined
afterEach(() => { mounted?.unmount() })
const context = { workspace: 'Production', provider: 'agents', path: '/org/workspace/providers/agents', view: '#/agents/scout/chat' }
describe('page references', () => {
  it('renders saved context as a readable disclosure without changing the message', async () => {
    const content = 'How are you?' + PAGE_CONTEXT_MARKER + JSON.stringify(context)
    mounted = await mountVue(ChatMessage, { message: { id: 'message', role: 'user', content, tools: [] } })
    expect(mounted.element.querySelector('.agents-body')?.textContent).toBe('How are you?')
    expect(mounted.element.querySelector('summary')?.textContent).toContain('agents / scout / chat')
    expect(mounted.element.querySelector('summary')?.textContent).toContain('Context ·')
    expect(mounted.element.querySelector('dl')?.textContent).toContain('Production')
    expect(mounted.element.querySelector('details')?.open).toBe(false)
    expect(mounted.element.textContent).not.toContain(PAGE_CONTEXT_MARKER.trim())
    expect(content).toContain(JSON.stringify(context))
  })
  it('renders a resource pointer and read-tool hint without copying resource contents', async () => {
    const resource = { apiVersion: 'infrastructure.railgrid.ai/v1alpha1', kind: 'Instance', resource: 'instances', name: 'payments', readTool: 'get_instance' }
    const content = 'Why is this failing?' + PAGE_CONTEXT_MARKER + JSON.stringify({ ...context, resource })
    mounted = await mountVue(ChatMessage, { message: { id: 'message', role: 'user', content, tools: [] } })
    expect(mounted.element.querySelector('summary')?.textContent).toContain('Instance · payments')
    expect(mounted.element.querySelector('details')?.textContent).toContain('get_instance')
    expect(mounted.element.querySelector('details')?.open).toBe(false)
    expect(splitPageContext(content).reference?.resource).toEqual(resource)
  })
  it('preserves malformed or unrelated text verbatim', () => {
    for (const suffix of ['not json', '{}', JSON.stringify({ ...context, path: 42 })]) {
      const content = 'Keep this' + PAGE_CONTEXT_MARKER + suffix
      expect(splitPageContext(content)).toEqual({ text: content })
    }
    expect(splitPageContext('Ordinary text')).toEqual({ text: 'Ordinary text' })
  })
})
