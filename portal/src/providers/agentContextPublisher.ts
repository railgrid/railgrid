import { normalizeProviderAgentContext, type PublishAgentContext } from '@/portalkit/agent-context'

/** The standard provider page mount can publish bounded context for Agents. */
export function createAgentContextPublisher(
  isCurrent: () => boolean,
  publish: PublishAgentContext,
): PublishAgentContext {
  return value => {
    if (!isCurrent()) return
    publish(value === null ? null : normalizeProviderAgentContext(value))
  }
}
