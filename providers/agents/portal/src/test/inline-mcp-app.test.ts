import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, onBeforeUnmount } from 'vue'
import AgentChat from '../views/AgentChat.vue'
import type { ApiClient, SSEEvent } from '../api'
import type { AgentArtifact, SavedCaptureCitation } from '../types'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, unmountVueApps } from './vue-helper'

const life = vi.hoisted(() => ({ unmounts: 0 }))
vi.mock('../components/McpAppViewer.vue', () => ({ default: defineComponent({ setup: () => { onBeforeUnmount(() => { life.unmounts += 1 }); return () => h('div', { 'data-live-mcp-viewer': 'true' }, 'Provider app mounted') } }) }))
let visible: Array<() => void> = []
afterEach(() => { unmountVueApps(); vi.unstubAllGlobals(); visible = []; life.unmounts = 0 })
const source = { label: 'Model', object: { group: 'models.test', version: 'v1', resource: 'models', name: 'sales', uid: 'model-uid' }, access: [{ group: 'models.test', version: 'v1', resource: 'models', name: 'sales', verb: 'get' }] }
const base: AgentArtifact = { id: 'result-1', agentName: 'scout', agentUID: 'agent-uid', owner: 'alice', runID: 'run-1', sessionID: 'session-1', toolCallID: 'call-1', toolName: 'compile', title: 'SQL preview', mediaType: 'application/json', digest: 'a'.repeat(64), size: 2, sources: [source], createdAt: '2026-10-22T12:00:00Z', evidenceStatus: 'source_revalidation_required' }
const authorized: AgentArtifact = { ...base, data: '{}', evidenceStatus: 'source_identity_and_access_revalidated', providerPresentation: { mcpApp: { resourceUri: 'ui://provider/result', mimeType: 'text/html;profile=mcp-app', html: '<html>private-app-sentinel</html>', sha256: 'b'.repeat(64) } } }
const captureWithBytes: AgentArtifact = { ...base, data: 'captured-private-data-sentinel', input: 'captured-private-input-sentinel', evidenceStatus: 'source_identity_and_access_revalidated', providerPresentation: authorized.providerPresentation }
async function setup(options: { history?: boolean; hint?: boolean; mismatch?: 'run' | 'session' | 'tool'; capture?: AgentArtifact; citation?: SavedCaptureCitation; read?: () => Promise<{ artifact: AgentArtifact }> } = {}) {
  vi.stubGlobal('IntersectionObserver', class { constructor(callback: (entries: unknown[]) => void) { visible.push(() => callback([{ isIntersecting: true }])) } observe() {} disconnect() {} })
  const captureAgentArtifact = vi.fn(async () => options.capture || base)
  const getAgentArtifact = vi.fn(options.read || (async () => ({ artifact: authorized })))
  const chatStream = vi.fn(async function* (): AsyncGenerator<SSEEvent> {
    yield { event: 'start', data: { runID: 'run-1', sessionID: 'session-1' } }
    yield { event: 'tool_start', data: { id: 'call-1', name: 'compile' } }
    yield { event: 'tool_end', data: {
      id: 'call-1', name: 'compile',
      runID: options.mismatch === 'run' ? 'run-other' : 'run-1',
      sessionID: options.mismatch === 'session' ? 'session-other' : 'session-1',
      toolCallID: options.mismatch === 'tool' ? 'call-old' : 'call-1',
      result: '{}', sources: [source], hasMcpApp: options.hint ?? true,
    } }
    if (options.citation) {
      yield { event: 'tool_start', data: { id: 'open-call-1', name: 'open_analysis_artifact', args: '{"artifactID":"result-1"}' } }
      yield { event: 'tool_end', data: { id: 'open-call-1', name: 'open_analysis_artifact', runID: 'run-1', sessionID: 'session-1', toolCallID: 'open-call-1', result: '{"opened":true}', savedCapture: options.citation } }
    }
    yield { event: 'done', data: { runID: 'run-1', content: `Compiled successfully [[tool:call-1]]${options.citation ? ' [[tool:open-call-1]]' : ''}`, status: 'completed' } }
  })
  const api = stubApi({ listSessions: async () => [{ id: 'session-1', preview: 'Current', messageCount: 0, createdAt: '', lastActivity: '' }], listMessagePage: async () => ({ items: options.history ? [
    { id: 'tool-row', role: 'tool', content: '{}', runID: 'run-1', createdAt: '2026-10-22T12:00:00Z', metadata: { tool: 'compile', toolCallID: 'call-1', sources: [source], hasMcpApp: true } },
    { id: 'answer-row', role: 'assistant', content: 'Compiled successfully [[tool:call-1]]', runID: 'run-1', createdAt: '2026-10-22T12:00:01Z', metadata: { turnPhase: 'final', turnStatus: 'completed' } },
  ] : [] }), listRuns: async () => ({ items: [] }), chatStream, captureAgentArtifact, getAgentArtifact }) as ApiClient
  const store = makeStore(api); const agent = agentFixture('scout'); agent.metadata.uid = 'agent-uid'; store.agents.data = [agent]; store.agents.loaded = true
  const view = await mountVue(AgentChat, { store, api, name: 'scout', compact: true }); await settleVue(10)
  if (options.history) return { view, getAgentArtifact, captureAgentArtifact, chatStream }
  const input = view.element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!; input.value = 'Compile sales'; input.dispatchEvent(new Event('input')); await settleVue()
  view.element.querySelector('form.agents-composer')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue(12)
  return { view, getAgentArtifact, captureAgentArtifact, chatStream }
}
async function reveal() { visible.forEach(callback => callback()); await settleVue(15) }

describe('inline provider app journey', () => {
  it('loads an authorized receipt, opens one full viewer beside the conversation, and rechecks on return', async () => {
    const { view, captureAgentArtifact, getAgentArtifact, chatStream } = await setup()
    expect(view.element.querySelector('.agents-body .agents-tool-citation')).toBeNull()
    expect(view.element.querySelector('.agents-body .agents-tool-citation-unavailable')).toBeNull()
    expect(view.element.querySelector('.agents-body')?.textContent).not.toContain('[[tool:')
    expect(captureAgentArtifact).not.toHaveBeenCalled(); await reveal()
    expect(captureAgentArtifact).toHaveBeenCalledExactlyOnceWith({ agent: 'scout', runID: 'run-1', sessionID: 'session-1', toolCallID: 'call-1', title: 'Compile' })
    expect(getAgentArtifact).toHaveBeenCalledExactlyOnceWith('scout', 'result-1')
    expect(view.element.querySelector('.agents-log [data-live-mcp-viewer]')).not.toBeNull()
    expect(view.element.querySelector('.agents-artifact-panel')).toBeNull()
    expect(view.element.querySelector<HTMLFormElement>('.agents-composer')!.style.display).not.toBe('none')
    expect(view.element.querySelector('.agents-inline-app')?.textContent).toContain('Provider')
    const expand = view.element.querySelector<HTMLButtonElement>('.agents-inline-app__open[aria-label="Expand result"]')!
    expand.click(); await settleVue(15)
    const panel = view.element.querySelector<HTMLElement>('[role="region"][aria-label="Result details"]')!
    expect(panel).not.toBeNull()
    expect(panel.querySelector('h2')?.textContent).toBe('Result details')
    expect(view.element.querySelector('.agents-inline-app__placeholder')?.textContent).toContain('Result open in details')
    expect(panel.querySelector('.agents-artifact-panel__data summary')?.textContent).toContain('View captured data')
    expect(panel.querySelector<HTMLDetailsElement>('.agents-artifact-panel__evidence')?.open).toBe(false)
    expect(panel.textContent).not.toContain('Access checked')
    expect(view.element.querySelectorAll('[data-live-mcp-viewer]')).toHaveLength(1)
    expect(view.element.querySelector<HTMLFormElement>('.agents-composer')).not.toBeNull()
    panel.querySelector<HTMLElement>('.agents-artifact-panel__data summary')!.click(); await settleVue(8)
    expect(panel.querySelector('.agents-artifact-panel__data pre')?.textContent).toBe('{}')
    expect(panel.querySelector<HTMLDetailsElement>('.agents-artifact-panel__evidence')?.open).toBe(false)
    panel.querySelector<HTMLButtonElement>('[aria-label="Back to conversation"]')!.click(); await settleVue(15)
    // A newly mounted lazy card gets a fresh observer; simulate it intersecting again.
    await reveal()
    expect(view.element.querySelector('.agents-artifact-panel')).toBeNull()
    expect(view.element.querySelector('.agents-inline-app [data-live-mcp-viewer]')).not.toBeNull()
    expect(getAgentArtifact).toHaveBeenCalledTimes(3)
    expect(view.element.querySelectorAll('[data-live-mcp-viewer]')).toHaveLength(1)
    expect(chatStream).toHaveBeenCalledTimes(1)
    expect(JSON.stringify(chatStream.mock.calls)).not.toContain('private-app-sentinel')
  })
  it('keeps capture bytes and app HTML hidden when source access is revoked before opening details', async () => {
    let reads = 0
    let rejectRevokedRead!: (reason?: unknown) => void
    const revokedRead = new Promise<{ artifact: AgentArtifact }>((_resolve, reject) => { rejectRevokedRead = reject })
    const { view, getAgentArtifact, chatStream } = await setup({
      capture: captureWithBytes,
      read: async () => {
        reads += 1
        if (reads === 1) return { artifact: authorized }
        return revokedRead
      },
    })
    await reveal()
    expect(view.element.querySelector('.agents-inline-app [data-live-mcp-viewer]')).not.toBeNull()
    view.element.querySelector<HTMLButtonElement>('[aria-label="Expand result"]')!.click()
    await settleVue(6)

    const panel = view.element.querySelector<HTMLElement>('[role="region"][aria-label="Result details"]')!
    expect(getAgentArtifact).toHaveBeenCalledTimes(2)
    expect(panel.textContent).toContain('Checking source access before opening the result')
    expect(panel.querySelector('[data-live-mcp-viewer]')).toBeNull()
    expect(panel.textContent).not.toContain('private-app-sentinel')
    expect(panel.textContent).not.toContain('captured-private-data-sentinel')
    expect(panel.textContent).not.toContain('captured-private-input-sentinel')
    expect(view.element.querySelector<HTMLFormElement>('.agents-composer')).not.toBeNull()
    rejectRevokedRead(new Error('source access revoked'))
    await settleVue(15)
    expect(panel.textContent).toContain('Result bytes stay hidden')
    expect(panel.querySelector('[data-live-mcp-viewer]')).toBeNull()
    expect(panel.textContent).not.toContain('private-app-sentinel')
    expect(panel.textContent).not.toContain('captured-private-data-sentinel')
    expect(panel.textContent).not.toContain('captured-private-input-sentinel')
    expect(view.element.querySelector<HTMLFormElement>('.agents-composer')).not.toBeNull()
    expect(view.element.textContent).toContain('Compiled successfully')
    expect(chatStream).toHaveBeenCalledTimes(1)
  })
  it('suspends the original full app while a cited subset is open in result details', async () => {
    const selection: SavedCaptureCitation = {
      artifactID: base.id,
      digest: base.digest,
      selection: { rowIndices: [0], fields: ['name'] },
    }
    const selectionArtifact: AgentArtifact = {
      ...authorized,
      data: '{"result":{"samples":[{"name":"north","privateFullCapture":"must-not-render"}]}}',
    }
    const { view, getAgentArtifact } = await setup({
      citation: selection,
      read: async () => ({ artifact: selectionArtifact }),
    })
    await reveal()
    expect(view.element.querySelector('.agents-inline-app [data-live-mcp-viewer]')).not.toBeNull()
    view.element.querySelector<HTMLButtonElement>('[data-agent-saved-capture-citation="open-call-1"]')!.click()
    await settleVue(15)

    const panel = view.element.querySelector<HTMLElement>('[role="region"][aria-label="Result details"]')!
    expect(getAgentArtifact).toHaveBeenCalledTimes(2)
    expect(panel.querySelector('.agents-artifact-panel__app')).toBeNull()
    expect(panel.querySelector('.agents-artifact-panel__data')).toBeNull()
    expect(panel.querySelector('.agents-artifact-panel__status')?.textContent).toContain('selected rows and fields')
    expect(panel.textContent).not.toContain('must-not-render')
    expect(view.element.querySelector('.agents-inline-app__placeholder')?.textContent).toContain('Result open in details')
    expect(view.element.querySelector('.agents-inline-app [data-live-mcp-viewer]')).toBeNull()
    expect(view.element.querySelectorAll('[data-live-mcp-viewer]')).toHaveLength(0)
    expect(view.element.querySelector<HTMLFormElement>('.agents-composer')).not.toBeNull()
  })
  it.each([
    { name: 'unverified', artifact: { ...authorized, evidenceStatus: 'source_revalidation_required' } },
    { name: 'different digest', artifact: { ...authorized, digest: 'c'.repeat(64) } },
    { name: 'different session', artifact: { ...authorized, sessionID: 'session-other' } },
    { name: 'different agent', artifact: { ...authorized, agentUID: 'agent-other' } },
  ])('never mounts $name data', async ({ artifact }) => {
    const { view } = await setup({ read: async () => ({ artifact }) }); await reveal()
    expect(view.element.querySelector('[data-live-mcp-viewer]')).toBeNull()
    expect(view.element.textContent).toContain('Compiled successfully')
    expect(view.element.textContent).not.toContain('private-app-sentinel')
  })
  it('does not capture tools without a server app hint', async () => {
    const { view, captureAgentArtifact } = await setup({
      hint: false,
      read: async () => ({ artifact: { ...authorized, providerPresentation: undefined } }),
    }); await reveal()
    expect(view.element.querySelector('.agents-inline-app')).toBeNull(); expect(captureAgentArtifact).not.toHaveBeenCalled()
    const citation = view.element.querySelector<HTMLButtonElement>('.agents-body [data-agent-tool-citation]')!
    expect(citation.textContent).toContain('Source')
    citation.click(); await settleVue(15)
    expect(captureAgentArtifact).toHaveBeenCalledOnce()
    const panel = view.element.querySelector('.agents-artifact-panel')!
    expect(panel).not.toBeNull()
    expect(panel.querySelector('.agents-artifact-panel__app')).toBeNull()
    expect(panel.querySelector('.agents-artifact-panel__data summary')?.textContent).toContain('View captured data')
  })
  it.each(['run', 'session', 'tool'] as const)('refuses app and source affordances for a mismatched %s receipt', async mismatch => {
    const { view, captureAgentArtifact } = await setup({ mismatch })
    await reveal()
    expect(view.element.querySelector('.agents-inline-app')).toBeNull()
    expect(view.element.querySelector('.agents-body .agents-tool-citation-unavailable')?.textContent).toBe('Source unavailable')
    expect(view.element.querySelector('.agents-body [data-agent-tool-citation]')).toBeNull()
    expect(captureAgentArtifact).not.toHaveBeenCalled()
  })
  it('restores the app from history without another model turn', async () => {
    const { view, chatStream, getAgentArtifact } = await setup({ history: true }); await reveal()
    expect(view.element.querySelector('.agents-log [data-live-mcp-viewer]')).not.toBeNull()
    expect(getAgentArtifact).toHaveBeenCalledExactlyOnceWith('scout', 'result-1')
    expect(chatStream).not.toHaveBeenCalled()
  })
  it('unmounts an open app when API authority changes', async () => {
    const { view } = await setup(); await reveal()
    expect(view.element.querySelector('[data-live-mcp-viewer]')).not.toBeNull()
    await view.setProps({ api: stubApi({ listSessions: async () => [], listMessagePage: async () => ({ items: [] }), listRuns: async () => ({ items: [] }) }) })
    await settleVue(12)
    expect(life.unmounts).toBe(1)
    expect(view.element.querySelector('[data-live-mcp-viewer]')).toBeNull()
  })
  it('discards an authorized response after the thread changes', async () => {
    let resolve!: (value: { artifact: AgentArtifact }) => void
    const deferred = new Promise<{ artifact: AgentArtifact }>(done => { resolve = done })
    const { view, getAgentArtifact } = await setup({ read: () => deferred }); await reveal()
    expect(getAgentArtifact).toHaveBeenCalledTimes(1)
    const newChat = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('New thread'))!
    newChat.click(); await settleVue(10); resolve({ artifact: authorized }); await settleVue(12)
    expect(view.element.querySelector('[data-live-mcp-viewer]')).toBeNull()
    expect(view.element.textContent).not.toContain('private-app-sentinel')
  })
})
