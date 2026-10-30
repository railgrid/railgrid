import type { ProviderAgentContext } from '@/portalkit/agent-context'
import type { PageResourceHint } from '@/portalkit/page-context'

export function pageResourceForScope(
  snapshot: { scope: string; resource: PageResourceHint } | null,
  scope: string,
): PageResourceHint | undefined {
  return snapshot?.scope === scope ? snapshot.resource : undefined
}

export function providerAgentContextForScope(
  snapshot: { scope: string; context: ProviderAgentContext } | null,
  scope: string,
): ProviderAgentContext | undefined {
  return snapshot?.scope === scope ? snapshot.context : undefined
}

/** Page context is opt-in at the point it is attached to a user message. */
export function pageContextForOverlay(followContext: boolean, context: string): string | undefined {
  return followContext ? context : undefined
}
