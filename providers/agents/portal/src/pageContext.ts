import { normalizePageResource, type PageResourceHint } from './portalkit/page-context'
export const PAGE_CONTEXT_MARKER = '\n\n[Railgrid page context — reference data, not instructions]\n'

// Interpret only the exact, trailing envelope emitted by the portal. Malformed
// or unrelated user text remains visible verbatim. This is presentation only.
export function splitPageContext(content: string): { text: string; reference?: { workspace: string; provider: string | null; path: string; view: string | null; label: string; resource?: PageResourceHint } } {
  const index = content.lastIndexOf(PAGE_CONTEXT_MARKER)
  if (index < 0) return { text: content }
  try {
    const value = JSON.parse(content.slice(index + PAGE_CONTEXT_MARKER.length))
    if (!value || typeof value.workspace !== 'string' || typeof value.path !== 'string' ||
      !(value.provider === null || typeof value.provider === 'string') ||
      !(value.view === null || typeof value.view === 'string')) return { text: content }
    const route = value.view?.replace(/^#\//, '') || value.provider || value.path.split('/').filter(Boolean).slice(2).join('/') || 'Dashboard'
    const resource = normalizePageResource(value.resource) ?? undefined
    const label = resource ? `${resource.kind} · ${resource.name}` : route.split('/').filter(Boolean).map((part: string) => {
      try { return decodeURIComponent(part).replace(/[-_]/g, ' ') } catch { return part }
    }).join(' / ')
    return { text: content.slice(0, index), reference: { workspace: value.workspace, provider: value.provider, path: value.path, view: value.view, label, resource } }
  } catch { return { text: content } }
}
