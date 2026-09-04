import { computed, type Ref } from 'vue'

import { createKueryApi, type KueryApi, type QuerySpec, type QueryStatus } from './api'
import type { FarosContext } from './element'
import { createKueryRequestContext } from './request-context'
import type { KueryRequestContext } from './request-context'

export { createKueryRequestContext }
export type { KueryRequestContext }

export function serviceBase(context: FarosContext | null): string {
  return createKueryRequestContext(context).basePath
}

export function tenantHeaders(context: FarosContext | null): Record<string, string> {
  return createKueryRequestContext(context).headers
}

export function useKueryApi(context: Ref<FarosContext | null>): { api: Readonly<Ref<KueryApi | null>>; query: (spec: QuerySpec, signal?: AbortSignal) => Promise<QueryStatus> } {
  const requestContext = computed(() => createKueryRequestContext(context.value))
  const api = computed(() => {
    const request = requestContext.value
    return request.basePath && request.token
      ? createKueryApi({ basePath: request.basePath, headers: request.headers })
      : null
  })
  return {
    api,
    query: async (spec, signal) => {
      if (!api.value) throw new Error('Kuery is waiting for workspace context')
      return api.value.query(spec, { signal })
    },
  }
}

export function errorMessage(error: unknown, recovery: string): string {
  if (error instanceof DOMException && error.name === 'AbortError') return ''
  const detail = error instanceof Error ? error.message : String(error)
  return `${detail}. ${recovery}`
}

export function edgeName(cluster = ''): string { return cluster.split('/').pop() || cluster || '—' }

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
