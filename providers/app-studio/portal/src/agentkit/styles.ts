// CANONICAL SOURCE — provider-sdk/agentkit. Do not edit vendored copies under
// providers/*/portal/src/agentkit/; edit here and run `make sync-portalkit`.

// AgentKit owns optional AI, workbench, and model presentation. It loads the
// core PortalKit handoff first because AgentKit recipes use the shared tokens
// and controls, while keeping a separate marker so the core stylesheet never
// implies that these optional recipes are present.
import agentUIStyles from './agent-ui.css?inline'
import activityStyles from './activity.css?inline'
import conversationStyles from './conversation.css?inline'
import { ensureFarosUIStyles } from '../portalkit/styles'

export const AGENT_UI_STYLE_ID = 'k-agent-ui'
export const AGENT_UI_CANONICAL_MARKER = '--faros-agent-ui-canonical'
export const AGENT_UI_CANONICAL_VALUE = '1'
export const AGENT_UI_VERSION_MARKER = '--faros-agent-ui-version'
export const AGENT_UI_VERSION = 5

function hasRequiredVersion(value: string): boolean {
  const version = Number(value.trim())
  return Number.isFinite(version) && version >= AGENT_UI_VERSION
}

function hostStylesAreLoaded(): boolean {
  const root = document.documentElement
  if (!root) return false

  if (typeof window !== 'undefined' && typeof window.getComputedStyle === 'function') {
    const styles = window.getComputedStyle(root)
    return styles.getPropertyValue(AGENT_UI_CANONICAL_MARKER).trim() === AGENT_UI_CANONICAL_VALUE
      && hasRequiredVersion(styles.getPropertyValue(AGENT_UI_VERSION_MARKER))
  }
  return root.style?.getPropertyValue(AGENT_UI_CANONICAL_MARKER).trim() === AGENT_UI_CANONICAL_VALUE
    && hasRequiredVersion(root.style?.getPropertyValue(AGENT_UI_VERSION_MARKER) || '')
}

/**
 * Load AgentKit's optional presentation exactly once for this document.
 * Importing this module has no side effects; callers opt in by invoking the
 * helper from an AgentKit component or application entrypoint.
 */
export function ensureAgentUIStyles(): void {
  if (typeof document === 'undefined') return

  ensureFarosUIStyles()
  if (hostStylesAreLoaded()) return

  const fallbackStyleID = document.getElementById(AGENT_UI_STYLE_ID)
    ? `${AGENT_UI_STYLE_ID}-v${AGENT_UI_VERSION}`
    : AGENT_UI_STYLE_ID
  if (document.getElementById(fallbackStyleID)) return

  const style = document.createElement('style')
  style.id = fallbackStyleID
  style.setAttribute('data-faros-agent-ui-source', 'agentkit-fallback')
  style.setAttribute('data-faros-agent-ui-version', String(AGENT_UI_VERSION))
  style.textContent = `${agentUIStyles}\n${activityStyles}\n${conversationStyles}`
  document.head?.appendChild(style)
}
