/**
 * Identity and lifecycle guards for App Studio project deletion.
 *
 * Project names are user-facing and mutable enough to be reused immediately
 * after a delete. Kubernetes UIDs are the immutable identity, while the
 * context/route fingerprint keeps an old action from crossing a tenant or
 * route transition. This module deliberately has no Vue or API dependency so
 * the async invariants can be exercised without mounting the full portal.
 */

export interface ProjectDeletionIdentity {
  name: string
  uid?: string
}

export interface ProjectDeletionResource extends ProjectDeletionIdentity {
  /** True when the API observed metadata.deletionTimestamp. */
  deleting?: boolean
}

export interface ProjectDeletionContext {
  /** The authenticated tenant/user authority fingerprint. */
  fingerprint: string
  /** The route at which the action was initiated. */
  routePath: string
}

export interface ProjectDeletionOperation {
  serial: number
  target: ProjectDeletionIdentity
  context: ProjectDeletionContext
}

export interface ProjectDeletionController {
  /** Capture immutable target and route/context before opening confirmation. */
  begin(target: ProjectDeletionIdentity, context: ProjectDeletionContext): ProjectDeletionOperation
  /** Invalidate an open/in-flight action without affecting acknowledged deletes. */
  invalidate(): void
  /** Check the operation's route/context fence. */
  isCurrent(operation: ProjectDeletionOperation, context: ProjectDeletionContext): boolean
  /** Check the operation fence and that the visible object is still the target. */
  matchesCurrent(
    operation: ProjectDeletionOperation,
    context: ProjectDeletionContext,
    current: ProjectDeletionIdentity | null | undefined,
  ): boolean
  /** Record server acceptance under the original authority scope. */
  acknowledge(operation: ProjectDeletionOperation): void
  /** True for API-deleting resources or an acknowledged target tombstone. */
  isDeleting(contextFingerprint: string, resource: ProjectDeletionResource): boolean
  /** True while an acknowledged or metadata-deleting object still needs a list reconciliation. */
  hasPending(contextFingerprint: string): boolean
  /**
   * Reconcile one complete project list against acknowledged tombstones.
   *
   * Every snapshot of a locally acknowledged UID stays hidden. A terminating
   * resource first discovered from the API remains visible and locked. Seeing
   * a different UID under the same name proves a replacement and clears the
   * old tombstone, so a same-name recreation remains visible.
   */
  reconcile<T extends ProjectDeletionResource>(contextFingerprint: string, resources: readonly T[]): T[]
  /** Query an acknowledgement directly (useful for name-only UI lookups). */
  has(contextFingerprint: string, resource: ProjectDeletionIdentity): boolean
  /** Clear all authority-scoped tombstones, normally on component teardown. */
  clear(): void
}

interface TombstoneScope {
  readonly byName: Map<string, {
    /** null means the old API did not provide a UID; retain by name. */
    uid: string | null
    /** Locally accepted deletes stay hidden; server-discovered deletes remain visible. */
    hidden: boolean
  }>
}

function normalizedUID(uid: string | undefined): string | undefined {
  const value = uid?.trim()
  return value || undefined
}

/**
 * Compare two project identities. If either side has a UID, both sides must
 * have the same UID; a same-name object with a missing/different UID is not
 * safe to treat as the original resource.
 */
export function sameProjectIdentity<T extends ProjectDeletionIdentity>(
  expected: T,
  current: ProjectDeletionIdentity | null | undefined,
): current is ProjectDeletionIdentity {
  if (!current || expected.name !== current.name) return false
  const expectedUID = normalizedUID(expected.uid)
  const currentUID = normalizedUID(current.uid)
  if (expectedUID || currentUID) return Boolean(expectedUID && currentUID && expectedUID === currentUID)
  // With no immutable identity, only the exact object reference is safe. This
  // keeps a refreshed same-name object from inheriting an old confirmation.
  return expected === current
}

function scopeFor(scopes: Map<string, TombstoneScope>, fingerprint: string, create = false): TombstoneScope | undefined {
  const existing = scopes.get(fingerprint)
  if (existing || !create) return existing
  const scope: TombstoneScope = { byName: new Map() }
  scopes.set(fingerprint, scope)
  return scope
}

export function createProjectDeletionController(): ProjectDeletionController {
  const scopes = new Map<string, TombstoneScope>()
  let serial = 0

  const has = (contextFingerprint: string, resource: ProjectDeletionIdentity): boolean => {
    const scope = scopeFor(scopes, contextFingerprint)
    const tombstone = scope?.byName.get(resource.name)
    if (tombstone === undefined) return false
    const currentUID = normalizedUID(resource.uid)
    // An unknown current UID cannot prove that the acknowledged object is
    // absent. The API now always returns UIDs, but this fail-closed fallback
    // protects older cached responses during an upgrade.
    return tombstone.uid === null || !currentUID || tombstone.uid === currentUID
  }

  return {
    begin(target, context) {
      return {
        serial: ++serial,
        target: { name: target.name, uid: normalizedUID(target.uid) },
        context: { fingerprint: context.fingerprint, routePath: context.routePath },
      }
    },

    invalidate() {
      serial += 1
    },

    isCurrent(operation, context) {
      return operation.serial === serial &&
        operation.context.fingerprint === context.fingerprint &&
        operation.context.routePath === context.routePath
    },

    matchesCurrent(operation, context, current) {
      return this.isCurrent(operation, context) && sameProjectIdentity(operation.target, current)
    },

    acknowledge(operation) {
      const scope = scopeFor(scopes, operation.context.fingerprint, true)!
      scope.byName.set(operation.target.name, {
        uid: normalizedUID(operation.target.uid) ?? null,
        hidden: true,
      })
    },

    isDeleting(contextFingerprint, resource) {
      return Boolean(resource.deleting) || has(contextFingerprint, resource)
    },

    hasPending(contextFingerprint) {
      return (scopeFor(scopes, contextFingerprint)?.byName.size ?? 0) > 0
    },

    reconcile<T extends ProjectDeletionResource>(contextFingerprint: string, resources: readonly T[]): T[] {
      const scope = scopeFor(scopes, contextFingerprint, true)!

      // A fresh server projection is authoritative for starting a deletion
      // marker, even if the local acknowledgement was lost during a reload.
      for (const resource of resources) {
        if (!resource.deleting || scope.byName.has(resource.name)) continue
        scope.byName.set(resource.name, {
          uid: normalizedUID(resource.uid) ?? null,
          hidden: false,
        })
      }

      const byName = new Map<string, ProjectDeletionResource>()
      for (const resource of resources) byName.set(resource.name, resource)
      for (const [name, tombstone] of [...scope.byName]) {
        const current = byName.get(name)
        if (!current) {
          // A complete list that omits the tombstoned object is the final
          // absence proof; permit a future same-name create to show normally.
          scope.byName.delete(name)
          continue
        }
        const currentUID = normalizedUID(current.uid)
        if (tombstone.uid !== null && currentUID && currentUID !== tombstone.uid) {
          // The name was reused by a new object. Do not hide the replacement.
          scope.byName.delete(name)
        }
      }

      return resources.filter(resource => {
        const marker = scope.byName.get(resource.name)
        // A delete accepted in this live session has already left the list and
        // must not flash back during finalizer cleanup. A deleting resource
        // first discovered from the API remains visible and locked so a reload
        // still presents truthful progress.
        return marker === undefined || !marker.hidden
      })
    },

    has,

    clear() {
      scopes.clear()
    },
  }
}
