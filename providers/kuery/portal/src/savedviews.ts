// SavedViews and edges through the kube client.
//
// kuery's portal used to ask the provider for both: GET /api/edges returned
// the engaged edge names, and there was nothing to list for SavedViews because
// nothing read them. Both are ordinary Kubernetes objects in the tenant's own
// workspace, so the portal reads them the way every other railgrid portal
// reads its provider's resources — through the hub's kcp proxy, as the
// signed-in user, with their own RBAC deciding what comes back. That deletes a
// provider route and makes the answer honest: what the user can see is what
// the user is allowed to see.

import { createKubeClient, type KubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import type { KueryRequestContext } from './request-context'

/** kuery's own exported kind. Cluster-scoped: the run verb addresses it by name alone. */
export const SAVEDVIEW_REF: KubeResourceRef = {
  group: 'kuery.providers.railgrid.ai',
  version: 'v1alpha1',
  resource: 'savedviews',
}

/**
 * The edges provider's kind, read straight from the tenant's own binding. The
 * portal never needs kuery's opinion about which edges exist — only about
 * which of them it has engaged, which is on the SavedView run's result.
 */
export const EDGE_REF: KubeResourceRef = {
  group: 'edges.railgrid.ai',
  version: 'v1alpha1',
  resource: 'kubernetesclusters',
}

export interface SavedView extends KubeObject {
  spec?: {
    displayName?: string
    description?: string
    query?: Record<string, unknown>
  }
  status?: {
    lastOpenedAt?: string
    conditions?: Array<{ type?: string; status?: string; reason?: string; message?: string }>
  }
}

export interface EdgeCluster extends KubeObject {
  status?: { connected?: boolean }
}

/** kubeClientFor builds the client for the context's workspace, or null. */
export function kubeClientFor(request: KueryRequestContext): KubeClient | null {
  if (!request.ready) return null
  return createKubeClient({ fetch: request.fetch, cluster: request.cluster, fieldManager: 'railgrid-kuery-portal' })
}

/** listSavedViews returns the workspace's views, most recently opened first. */
export async function listSavedViews(kube: KubeClient): Promise<SavedView[]> {
  const views = await kube.listAll<SavedView>(SAVEDVIEW_REF)
  return views.sort((a, b) => {
    const left = a.status?.lastOpenedAt ?? ''
    const right = b.status?.lastOpenedAt ?? ''
    if (left !== right) return right.localeCompare(left)
    return (a.metadata.name || '').localeCompare(b.metadata.name || '')
  })
}

/**
 * listEdges returns the workspace's connected KubernetesCluster edges by name.
 *
 * Only connected ones: a disconnected edge is not queryable (kuery disengages
 * it), so offering it in an edge selector would produce a query that
 * legitimately returns nothing and looks like a bug.
 */
export async function listEdges(kube: KubeClient): Promise<string[]> {
  const edges = await kube.listAll<EdgeCluster>(EDGE_REF)
  return edges
    .filter(edge => edge.status?.connected !== false)
    .map(edge => edge.metadata.name)
    .filter(Boolean)
    .sort()
}

/** isReady reads a SavedView's Ready condition, which the reconciler stamps. */
export function isReady(view: SavedView): boolean {
  const condition = view.status?.conditions?.find(entry => entry.type === 'Ready')
  return condition?.status === 'True'
}

/** notReadyReason is the reconciler's explanation, for a view that will not run. */
export function notReadyReason(view: SavedView): string {
  const condition = view.status?.conditions?.find(entry => entry.type === 'Ready')
  return condition?.status === 'False' ? condition.message || condition.reason || 'The saved query is not valid.' : ''
}

/** The fixed part of a per-user scratch view's name; mirrors queryapi.PlaygroundNamePrefix. */
export const PLAYGROUND_PREFIX = 'playground-'

/**
 * playgroundViewName derives the caller's scratch SavedView name, and must
 * match queryapi.PlaygroundViewName in the provider exactly — the provider
 * falls back to the same name for an MCP caller, so one user has one scratch
 * view whichever surface they arrive on.
 *
 * The first twelve hex characters of sha256(lowercased identity). A digest
 * rather than the address itself because an object name is readable by anyone
 * who can list the workspace, which is a wider audience than the people
 * entitled to know who has an account.
 */
export async function playgroundViewName(user: string): Promise<string> {
  const identity = (user || '').trim().toLowerCase()
  if (!identity) return `${PLAYGROUND_PREFIX}anonymous`
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(identity))
  const hex = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
  return PLAYGROUND_PREFIX + hex.slice(0, 12)
}

/**
 * ensurePlaygroundView creates the caller's scratch view if it is absent, as
 * the caller. It carries no query: every run supplies one as the request's
 * input override, so the object is a name and a grant surface rather than a
 * store.
 *
 * Idempotent, and tolerant of the race with another tab: AlreadyExists is the
 * success case.
 */
export async function ensurePlaygroundView(kube: KubeClient, name: string, user: string): Promise<void> {
  try {
    await kube.get(SAVEDVIEW_REF, name)
    return
  } catch (error) {
    if (!isNotFound(error)) throw error
  }
  try {
    await kube.create<SavedView>(SAVEDVIEW_REF, {
      apiVersion: `${SAVEDVIEW_REF.group}/${SAVEDVIEW_REF.version}`,
      kind: 'SavedView',
      metadata: { name },
      spec: {
        displayName: user ? `Query playground — ${user}` : 'Query playground',
        description: 'Scratch view for ad-hoc queries from the portal playground and the kuery MCP tools. Each run supplies its own query.',
      },
    })
  } catch (error) {
    if (!isAlreadyExists(error)) throw error
  }
}

function isNotFound(error: unknown): boolean {
  return !!error && typeof error === 'object' && (error as { status?: number }).status === 404
}

function isAlreadyExists(error: unknown): boolean {
  return !!error && typeof error === 'object' && (error as { reason?: string }).reason === 'AlreadyExists'
}
