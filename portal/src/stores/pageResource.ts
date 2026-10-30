import { shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { normalizePageResource, type PageResourceHint } from '@/portalkit/page-context'
import { normalizeProviderAgentContext, type ProviderAgentContext } from '@/portalkit/agent-context'

export const usePageResourceStore = defineStore('page-resource', () => {
  const snapshot = shallowRef<{ scope: string; resource: PageResourceHint } | null>(null)
  const agentContextSnapshot = shallowRef<{ scope: string; context: ProviderAgentContext } | null>(null)
  let resourceOwner: object | null = null
  let agentContextOwner: object | null = null
  function publish(source: object, scope: string, value: unknown): void {
    const resource = normalizePageResource(value)
    resourceOwner = source
    snapshot.value = resource ? Object.freeze({ scope, resource: Object.freeze(resource) }) : null
  }
  function publishAgentContext(source: object, scope: string, value: unknown): void {
    const context = value === null ? null : normalizeProviderAgentContext(value)
    agentContextOwner = source
    agentContextSnapshot.value = context ? Object.freeze({ scope, context }) : null
  }
  function clear(source: object): void {
    if (resourceOwner === source) {
      resourceOwner = null
      snapshot.value = null
    }
    if (agentContextOwner === source) {
      agentContextOwner = null
      agentContextSnapshot.value = null
    }
  }
  return { snapshot, agentContextSnapshot, publish, publishAgentContext, clear }
})
