import { computed, type Ref } from 'vue'

import { createKueryApi, type KueryApi, type QuerySpec, type QueryStatus } from './api'
import type { RailgridContext } from './element'
import { createKueryRequestContext } from './request-context'
import type { KueryRequestContext } from './request-context'

export { createKueryRequestContext }
export type { KueryRequestContext }

export function serviceBase(context: RailgridContext | null): string {
  return createKueryRequestContext(context).basePath
}

export function tenantHeaders(context: RailgridContext | null): Record<string, string> {
  return createKueryRequestContext(context).headers
}

/**
 * useKueryApi builds the query client for the current context.
 *
 * Every query is the run verb on a SavedView, so the client needs a view to
 * run as. A caller that is showing a saved view passes its name; the default
 * is the signed-in user's scratch view, which the playground creates with the
 * kube client on first use. That is what keeps ad-hoc queries inside the
 * authorization contract instead of beside it.
 */
export function useKueryApi(
  context: Ref<RailgridContext | null>,
  savedView?: Ref<string>,
): { api: Readonly<Ref<KueryApi | null>>; query: (spec: QuerySpec, signal?: AbortSignal, view?: string) => Promise<QueryStatus> } {
  const requestContext = computed(() => createKueryRequestContext(context.value))
  const api = computed(() => {
    const request = requestContext.value
    const view = savedView?.value || ''
    // request.ready is a transport-and-workspace check, never a token check:
    // the host injects Authorization into its own fetch, and gating on the
    // deprecated railgridContext.token would strand the portal the day a host
    // stops exposing it.
    return request.ready && view
      ? createKueryApi({
          basePath: request.basePath,
          cluster: request.cluster,
          savedView: view,
          headers: request.headers,
          fetch: request.fetch,
        })
      : null
  })
  return {
    api,
    query: async (spec, signal, view) => {
      if (!api.value) throw new Error('Kuery is waiting for workspace context')
      return api.value.query(spec, { signal, savedView: view })
    },
  }
}

export function errorMessage(error: unknown, recovery: string): string {
  if (error instanceof DOMException && error.name === 'AbortError') return ''
  const detail = error instanceof Error ? error.message : String(error)
  return `${detail}. ${recovery}`
}

export function edgeName(cluster = ''): string { return cluster.split('/').pop() || cluster || '—' }

/** notReady is the message a view that will not run shows instead of results. */
export function notReady(reason: string): string {
  return reason ? `This saved view will not run: ${reason}` : ''
}

export function resourceLabel(row: { object?: { kind?: string; metadata?: { namespace?: string; name?: string } } }): string {
  const object = row.object ?? {}
  const metadata = object.metadata ?? {}
  return `${object.kind || 'Object'} ${metadata.namespace ? `${metadata.namespace}/` : ''}${metadata.name || '?'}`
}

export function age(timestamp?: string): string {
  if (!timestamp) return '—'
  const milliseconds = Date.now() - new Date(timestamp).getTime()
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return '—'
  const minutes = Math.floor(milliseconds / 60_000)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  return hours < 48 ? `${hours}h` : `${Math.floor(hours / 24)}d`
}
