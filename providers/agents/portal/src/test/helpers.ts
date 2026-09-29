// Shared test fixtures: a store backed by a stub ApiClient, and a mount helper
// that waits for the public Vue custom-element host to settle. updateComplete
// remains part of that host's compatibility boundary for focused tests.

import type { ApiClient } from '../api'
import { AppStore } from '../store'
import type { Agent } from '../types'

export type StubApi = Partial<Record<keyof ApiClient, unknown>>

// stubApi fills in the loaders every store slice calls so a component under
// test only has to override the endpoints it exercises.
export function stubApi(overrides: StubApi = {}): ApiClient {
  const empty = () => Promise.resolve([])
  return {
    hasWorkspace: () => true,
    tenantKey: () => 'org/ws',
    tenant: () => ({ orgUUID: 'org', workspaceUUID: 'ws' }),
    context: () => null,
    setContext: () => undefined,
    listAgents: empty,
    listConnections: empty,
    listToolsets: empty,
    listSchedules: empty,
    listTriggers: empty,
    listCredentials: empty,
    listEdges: empty,
    listInbox: empty,
    listSessions: empty,
    listMessages: empty,
    oauthProviders: () => Promise.resolve({ providers: {} }),
    capabilities: () => Promise.resolve({ providers: [] }),
    ...overrides,
  } as unknown as ApiClient
}

export function makeStore(api: ApiClient): AppStore {
  return new AppStore(api)
}

/**
 * agentFixture builds an Agent the way kcp serves one. The default is a
 * MODEL-backed agent with a chat credential, which is what spec.backend says
 * now — the old spec.models / spec.modelFallbacks no longer exist on the CRD.
 */
export function agentFixture(name = 'scout', spec: Partial<Agent['spec']> = {}): Agent {
  return {
    metadata: { name },
    spec: { displayName: name, backend: { type: 'model', model: { credentials: { chat: 'openai' } } }, ...spec },
  }
}

/** modelBackend is the spec.backend of a model-backed agent, for a fixture. */
export function modelBackend(chat: string, fallbacks?: string[]): Agent['spec']['backend'] {
  return { type: 'model', model: { credentials: { chat }, ...(fallbacks ? { fallbacks } : {}) } }
}

// mount creates the element, assigns properties, appends it and waits for the
// whole subtree to finish its first update.
export async function mount<T extends HTMLElement>(tag: string, props: Record<string, unknown>): Promise<T> {
  const el = document.createElement(tag) as T
  Object.assign(el, props)
  document.body.appendChild(el)
  await settle(el)
  return el
}

// settle flushes pending updates across the custom-element host and its Vue
// subtree; a couple of passes covers parent → child property propagation.
export async function settle(el: HTMLElement, passes = 4): Promise<void> {
  for (let i = 0; i < passes; i++) {
    const root = el as HTMLElement & { updateComplete?: Promise<unknown> }
    if (root.updateComplete) await root.updateComplete
    await Promise.resolve()
    await new Promise((r) => setTimeout(r, 0))
    for (const child of el.querySelectorAll('*')) {
      const c = child as HTMLElement & { updateComplete?: Promise<unknown> }
      if (c.updateComplete) await c.updateComplete
    }
  }
}

export function text(el: Element | null | undefined): string {
  return (el?.textContent || '').replace(/\s+/g, ' ').trim()
}
