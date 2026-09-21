import { isNavigationFailure, NavigationFailureType, type Router, type RouteLocationNormalized } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAdminStore } from '@/stores/admin'
import { useTenantStore } from '@/stores/tenant'
import { useRouteContextStore } from '@/stores/routeContext'
import { parsePortalScope, scopedPath } from '@/portalkit/navigation'
import { rememberPortalNext } from '@/auth/portalNext'
import { readLandingScope, rememberLandingScope } from './landingPreference'

export function installContextGuard(router: Router): void {
  let navigation = 0
  const attempts = new WeakMap<RouteLocationNormalized, number>()

  function restoreCommittedContext(): void {
    navigation++
    const committed = router.currentRoute.value
    const context = useRouteContextStore()
    context.invalidate()
    context.destination = committed.fullPath
    const scope = parsePortalScope(committed.path)
    if (scope && !committed.meta.public && useAuthStore().token) void context.resolve(scope, true)
    else useAuthStore().setClusterName(null)
  }

  // Context resolution precedes lazy component loading. A failed import or
  // later guard rejection must restore the route Vue actually kept. Ignore
  // failures from superseded navigations so they cannot undo newer context.
  router.onError((_error, to) => {
    if (attempts.get(to) === navigation) restoreCommittedContext()
  })
  // Vue Router skips guards when the user returns to the already-current URL
  // while another navigation is pending. Cancel that pending authority read too.
  router.afterEach((to, _from, failure) => {
    const committedScope = parsePortalScope(to.path)
    if (!failure && committedScope && useRouteContextStore().state === 'ready') {
      rememberLandingScope(useAuthStore().user, committedScope)
    }
    // Unscoped destinations have no context resolver to hold the loading
    // gate. Release it only after the route commits, so an outgoing login
    // page cannot remount and redirect back to landing in the meantime.
    if (!failure && !committedScope) {
      useRouteContextStore().invalidate()
      return
    }
    if (isNavigationFailure(failure, NavigationFailureType.aborted) && attempts.get(to) === navigation) {
      restoreCommittedContext()
      return
    }
    if (!isNavigationFailure(failure, NavigationFailureType.duplicated)) return
    const context = useRouteContextStore()
    if (context.state !== 'loading' && context.destination === to.fullPath) return
    navigation++
    context.invalidate()
    context.destination = to.fullPath
    const scope = parsePortalScope(to.path)
    if (scope && !to.meta.public) void context.resolve(scope, true)
  })
  router.beforeEach(async (to) => {
    const attempt = ++navigation
    attempts.set(to, attempt)
    const current = () => attempt === navigation
    const context = useRouteContextStore()
    context.destination = to.fullPath
    const scope = parsePortalScope(to.path)
    if (to.meta.public || !scope || context.state !== 'ready' ||
      scope.orgUUID !== context.target?.orgUUID || scope.workspaceUUID !== context.target?.workspaceUUID) {
      context.invalidate()
      if (!to.meta.public) context.state = 'loading'
    }
    const tenant = useTenantStore()
    tenant.routeManaged = true
    const auth = useAuthStore()
    if (!to.meta.public) {
      // A hard refresh restores the portal bearer synchronously from storage,
      // but its HttpOnly browser session is re-established asynchronously.
      // Wait for that bootstrap before mounting provider routes; otherwise a
      // private preview iframe can enter app authorization first and bounce
      // through embedded login before the shared session exists.
      await auth.detectAuthMode()
      if (!current()) return false
      // A bearer authenticates the portal caller; a cluster target is selected
      // separately by the workspace control and is intentionally empty on
      // organization-only routes.
      if (!auth.token) {
        rememberPortalNext(to.fullPath)
        return { name: 'login', query: { returnTo: to.fullPath } }
      }
    }
    // Admin-only routes: confirm access before loading. Non-admins are bounced to
    // the dashboard so the page never mounts and never fires admin data fetches.
    if (to.meta.admin) {
      const admin = useAdminStore()
      const ok = admin.isAdmin === null ? await admin.checkAccess() : admin.isAdmin
      if (!current()) return false
      if (!ok) return { name: 'landing' }
    }
    if (to.name === 'landing') {
      context.invalidate()
      context.state = 'loading'
      try {
        await tenant.fetchOrgs()
        if (!current()) return false
        const availableOrgs = tenant.orgs.filter((item) => !item.deletionRequestedAt)
        if (tenant.orgLoadState !== 'ready') return { name: 'organizations' }
        // Resume the last visited scope only after rechecking membership.
        // Active state supports existing sessions; the per-account preference
        // also survives the identity reset on sign-out and subsequent sign-in.
        const remembered = tenant.orgUUID
          ? { orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID }
          : readLandingScope(auth.user)
        const rememberedOrg = availableOrgs.find((item) => item.uuid === remembered?.orgUUID)
        const org = rememberedOrg ?? (availableOrgs.length === 1 ? availableOrgs[0] : null)
        if (!org) return { name: 'organizations' }
        if (rememberedOrg && !remembered?.workspaceUUID) return `/${org.uuid}/settings/workspaces`
        await tenant.fetchWorkspaces(org.uuid, { selectDefault: false })
        if (!current()) return false
        const list = tenant.workspaceLoadStateByOrg[org.uuid] === 'ready' ? tenant.workspacesByOrg[org.uuid] ?? [] : []
        const workspace = rememberedOrg
          ? list.find((item) => item.uuid === remembered?.workspaceUUID && item.clusterName && !item.deletionRequestedAt)
          : list.find((item) => item.clusterName && !item.deletionRequestedAt)
        return scopedPath(workspace ? '/' : '/settings/workspaces', { orgUUID: org.uuid, workspaceUUID: workspace?.uuid ?? null })
      } catch {
        if (!current()) return false
        return { name: 'organizations' }
      }
    }
    if (scope && !to.meta.public) return await context.resolve(scope) && current() ? undefined : false
    // Unscoped destinations release their loading gate in afterEach.
  })
}
