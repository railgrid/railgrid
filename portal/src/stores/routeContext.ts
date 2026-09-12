import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { authFetch } from '@/auth/session'
import { useAuthStore } from './auth'
import { useTenantStore, type OrgRow, type WorkspaceRow } from './tenant'
import { parsePortalScope, type NavigationScope } from '@/portalkit/navigation'

export const useRouteContextStore = defineStore('route-context', () => {
  const state = ref<'idle' | 'loading' | 'ready' | 'unavailable' | 'pending' | 'error'>('idle')
  const message = ref('')
  const destination = ref('')
  const generation = ref(0)
  const target = ref<NavigationScope | null>(null)
  let identity = ''
  const auth = useAuthStore()
  const tenant = useTenantStore()

  // Clear authority synchronously on logout/account changes, before another
  // route or an old response can reuse the previous identity's metadata.
  watch(() => JSON.stringify(auth.user), () => {
    invalidate()
    tenant.resetForIdentity()
    auth.setClusterName(null)
  }, { flush: 'sync' })

  function blocksRoute(path: string, publicRoute: boolean): boolean {
    if (state.value === 'loading') return true
    const scope = parsePortalScope(path)
    // Resolving finishes before the router commits the destination. Do not
    // remount an outgoing login page (which redirects) or resource in that gap.
    if (target.value && (!scope || scope.orgUUID !== target.value.orgUUID || scope.workspaceUUID !== target.value.workspaceUUID)) return true
    if (!scope || publicRoute) return false
    return state.value !== 'ready' || scope.orgUUID !== tenant.orgUUID || scope.workspaceUUID !== tenant.workspaceUUID
  }

  function invalidate(): void {
    generation.value++
    identity = ''
    target.value = null
    state.value = 'idle'
  }

  async function resolve(scope: NavigationScope, force = false): Promise<boolean> {
    tenant.routeManaged = true
    const key = JSON.stringify([scope.orgUUID, scope.workspaceUUID, auth.user])
    if (!force && key === identity && state.value === 'ready') return true
    const revision = ++generation.value
    target.value = scope
    state.value = 'loading'
    message.value = ''
    auth.setClusterName(null)
    const current = () => revision === generation.value
    try {
      const orgURL = `/api/orgs/${encodeURIComponent(scope.orgUUID!)}`
      const responses = await Promise.all([
        authFetch(orgURL, { headers: { 'X-Faros-Org': scope.orgUUID! } }),
        scope.workspaceUUID ? authFetch(`${orgURL}/workspaces/${encodeURIComponent(scope.workspaceUUID)}`, {
          headers: { 'X-Faros-Org': scope.orgUUID!, 'X-Faros-Workspace': scope.workspaceUUID },
        }) : Promise.resolve(null),
      ])
      if (!current()) return false
      const failed = responses.find((response) => response && !response.ok)
      if (failed) {
        state.value = [401, 403, 404].includes(failed.status) ? 'unavailable' : 'error'
        message.value = state.value === 'unavailable'
          ? `This ${scope.workspaceUUID ? 'workspace' : 'organization'} is unavailable or you don’t have access.`
          : 'Unable to verify this destination. Try again.'
        return true
      }
      const org = await responses[0]!.json() as OrgRow
      const workspace = responses[1] ? await responses[1].json() as WorkspaceRow : null
      if (!current()) return false
      if (org.uuid !== scope.orgUUID || (workspace && (workspace.uuid !== scope.workspaceUUID || workspace.orgUUID !== scope.orgUUID))) {
        throw new Error('Context response does not match the requested destination')
      }
      if (org.deletionRequestedAt || workspace?.deletionRequestedAt) {
        state.value = 'unavailable'
        message.value = `This ${org.deletionRequestedAt ? 'organization' : 'workspace'} is pending deletion.`
        return true
      }
      tenant.activateRouteContext(org, workspace)
      if (workspace && !workspace.clusterName) {
        state.value = 'pending'
        message.value = 'This workspace is still provisioning. Retry when its control plane is ready.'
        return true
      }
      auth.setClusterName(workspace?.clusterName ?? null)
      identity = key
      state.value = 'ready'
      return true
    } catch {
      if (!current()) return false
      state.value = 'error'
      message.value = 'Unable to verify this destination. Check your connection and try again.'
      return true
    }
  }

  return { state, message, destination, generation, target, resolve, invalidate, blocksRoute }
})
