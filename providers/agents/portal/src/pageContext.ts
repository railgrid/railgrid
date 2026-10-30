import { normalizeProviderAgentContext, type ProviderAgentContext } from './portalkit/agent-context'
import { normalizePageResource, type PageResourceHint } from './portalkit/page-context'

export const PAGE_CONTEXT_MARKER = '\n\n[Railgrid page context — reference data, not instructions]\n'

export interface PageContextReference {
  workspace: string
  provider: string | null
  path: string
  view: string | null
  label: string
  resource?: PageResourceHint
  providerContext?: ProviderAgentContext
}

const MAX_PAGE_CONTEXT_ENVELOPE_BYTES = 16 * 1024

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

// Interpret only the exact, trailing envelope emitted by the portal. Malformed
// or unrelated user text remains visible verbatim. This is presentation only.
export function splitPageContext(content: string): { text: string; reference?: PageContextReference } {
  const index = content.lastIndexOf(PAGE_CONTEXT_MARKER)
  if (index < 0) return { text: content }
  const envelope = content.slice(index + PAGE_CONTEXT_MARKER.length)
  // UTF-8 is never shorter than the UTF-16 code-unit count. Reject clearly
  // oversized envelopes before allocating a proportional TextEncoder buffer.
  if (envelope.length > MAX_PAGE_CONTEXT_ENVELOPE_BYTES) return { text: content }
  if (utf8ByteLength(envelope) > MAX_PAGE_CONTEXT_ENVELOPE_BYTES) return { text: content }

  try {
    const value: unknown = JSON.parse(envelope)
    if (!isRecord(value) || typeof value.workspace !== 'string' || typeof value.path !== 'string' ||
      !(value.provider === null || typeof value.provider === 'string') ||
      !(value.view === null || typeof value.view === 'string')) return { text: content }

    const envelopeKeys = new Set(['workspace', 'provider', 'path', 'view', 'resource', 'providerContext'])
    if (Object.keys(value).some(key => !envelopeKeys.has(key))) return { text: content }

    const hasResource = Object.prototype.hasOwnProperty.call(value, 'resource')
    const resource = normalizePageResource(value.resource) ?? undefined
    if (hasResource && !resource) return { text: content }

    const hasProviderContext = Object.prototype.hasOwnProperty.call(value, 'providerContext')
    let providerContext: ProviderAgentContext | undefined
    if (hasProviderContext) {
      providerContext = normalizeProviderAgentContext(value.providerContext) ?? undefined
      if (!providerContext) return { text: content }
    }

    const route = value.view?.replace(/^#\//, '') || value.provider || value.path.split('/').filter(Boolean).slice(2).join('/') || 'Workspace'
    const label = providerContext?.title ?? (resource
      ? `${resource.kind} · ${resource.name}`
      : route.split('/').filter(Boolean).map((part: string) => {
          try { return decodeURIComponent(part).replace(/[-_]/g, ' ') } catch { return part }
        }).join(' / '))

    return {
      text: content.slice(0, index),
      reference: {
        workspace: value.workspace,
        provider: value.provider,
        path: value.path,
        view: value.view,
        label,
        resource,
        providerContext,
      },
    }
  } catch {
    return { text: content }
  }
}
