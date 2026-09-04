<script setup lang="ts">
import { computed, onMounted, onUnmounted, watch, watchEffect } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useProvidersStore } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { useRoute, useRouter } from 'vue-router'
import { registerProviderRoutes } from '@/router/providers'
import { SESSION_EXPIRED_EVENT } from '@/composables/useGraphQL'
import { useLayoutInsets } from '@/composables/useLayoutInsets'
import { toastBottomOffsetPx } from '@/composables/useToastBottomOffset'
import ControlPlaneProvisioning from '@/components/ControlPlaneProvisioning.vue'
import TerminalDock from '@/components/TerminalDock.vue'
import PkConfirmDialog from '@/portalkit/ConfirmDialog.vue'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import ToastHost from '@/portalkit/ToastHost.vue'

const auth = useAuthStore()
const providers = useProvidersStore()
const tenant = useTenantStore()
const terminal = useTerminalSessionsStore()
const layoutInsets = useLayoutInsets()
const route = useRoute()
const router = useRouter()

// A portal bearer authenticates the caller; auth.clusterName is only the
// current workspace's GraphQL target. Organization-only screens therefore
// remain authenticated while the target is intentionally empty.
const hasPortalSession = computed(() => !!auth.token)

// Keep the terminal singleton mounted so sessions survive navigation, but do
// not expose its chrome on the standalone organization chooser.
const hideTerminalDock = computed(
  () => route.path === '/organizations' || route.path.startsWith('/organizations/'),
)

// Register the dynamic provider route shape exactly once at app boot, before
// any deep link like /providers/foo can be resolved.
registerProviderRoutes(router)

// First-login takeover: while the hub is still provisioning the user's
// personal org + first workspace, tenant.bootstrap() keeps bootstrapState
// at 'provisioning' and we cover the whole app with the "creating control
// plane" screen. Returning users (cached selection) flip straight to
// 'ready' inside bootstrap(), so this never flashes for them.
const showProvisioning = computed(
  () => hasPortalSession.value && tenant.bootstrapState === 'provisioning',
)

// TenantSettingsPage and the organization chooser own their contextual error
// surfaces. Other routes still need a shell-level destination for a tenant
// operation failure, so keep one inline notification as the cross-route
// fallback rather than turning store errors into duplicate toasts.
const showTenantErrorInline = computed(() => {
  if (!hasPortalSession.value || !tenant.error) return false
  return !route.path.startsWith('/settings') && !route.path.startsWith('/organizations')
})

// ToastHost teleports its visual stack to <body>, outside AppLayout's DOM
// subtree. Publish the same shell clearance at the document root so the host
// follows a bottom navigation dock and the persistent TerminalDock without
// requiring either component to know about toast rendering.
const toastBottomOffset = computed(() => `${toastBottomOffsetPx({
  navigationBottom: layoutInsets.bottom,
  terminalVisible: !hideTerminalDock.value && terminal.isVisible,
  terminalSessionCount: terminal.sessions.length,
  terminalHeight: terminal.panelState.height,
  terminalMinimized: terminal.panelState.isMinimized,
  terminalFullscreen: terminal.panelState.isFullscreen,
})}px`)

watchEffect(() => {
  if (typeof document === 'undefined') return
  document.documentElement.style.setProperty('--k-toast-bottom-offset', toastBottomOffset.value)
})

// A dead gateway session (401/403/404) is detected deep inside
// useGraphQL, which can't import `@/router` without dragging the whole
// SPA into provider bundles. It signals here instead; the shell owns
// the logout + redirect. `replace`, not `push`, so Back doesn't return
// to the page that just failed to authenticate.
function onSessionExpired() {
  auth.logout()
  void router.replace({ name: 'login' })
}

onMounted(async () => {
  window.addEventListener(SESSION_EXPIRED_EVENT, onSessionExpired)
  await auth.detectAuthMode()
  if (hasPortalSession.value) {
    providers.load()
    tenant.bootstrap()
  }
})

onUnmounted(() => {
  window.removeEventListener(SESSION_EXPIRED_EVENT, onSessionExpired)
  if (typeof document !== 'undefined') {
    document.documentElement.style.removeProperty('--k-toast-bottom-offset')
  }
})

// Tenant errors belong to the route that initiated the operation. Clear them
// as navigation leaves that context so a failed settings mutation cannot
// reappear later as a shell announcement on an unrelated destination.
watch(
  () => route.fullPath,
  (path, previousPath) => {
    if (path !== previousPath) tenant.clearError()
  },
)

// Authentication can arrive after onMounted (token login form). Watch the
// bearer rather than the workspace target so organization-only state does not
// look like a logged-out session.
watch(
  () => auth.token,
  (ok) => {
    if (!ok) return
    if (!providers.loaded) providers.load()
    tenant.bootstrap()
  },
)

// Tenant → auth bridge: the shell's workspace switcher changes the active
// workspace in the tenant store, but every `/graphql/{cluster}`
// query is built from auth.clusterName. Without this sync the user switches
// workspace and the MCP/edges/workload pages keep showing data
// from the login-time DefaultCluster. Mirror activeWorkspace.clusterName →
// auth.clusterName so:
//   1. useGraphQLQuery's watchEffect (which reads auth.isAuthenticated, a
//      getter over s.clusterName) re-fires and re-queries the new cluster.
//   2. ProviderFrame's watch on auth.clusterName pushes a fresh
//      farosContext to the mounted provider element; its auth-adapter
//      hydrates and its useGraphQLQuery re-fires the same way.
// The hub omits clusterName until the workspace reports Ready. Keep the
// retained login cluster during the initial async hydration (this watcher is
// intentionally not immediate), but clear it as soon as an explicit org-only
// selection leaves no active workspace. This prevents workspace-scoped pages
// from continuing to target the previous org's cluster.
watch(
  () => tenant.activeWorkspace?.clusterName,
  (cluster) => {
    auth.setClusterName(cluster ?? null)
  },
)

// A persisted organization-only selection starts with no activeWorkspace, so
// the bridge above has no value transition to observe on a hard refresh. Once
// bootstrap begins hydrating that authority boundary (or reports an error),
// clear any login-time cluster target; a workspace-scoped route must never
// continue using the previous organization's cluster in the meantime.
watch(
  () => [tenant.orgUUID, tenant.workspaceMode, tenant.workspaceSelectionHydrated, tenant.workspaceLoadState] as const,
  ([, mode, hydrated]) => {
    if (mode === 'organization' || hydrated) {
      auth.setClusterName(tenant.activeWorkspace?.clusterName ?? null)
    }
  },
)

// The provider catalog includes organization-owned registrations, while the
// enabled binding map is workspace-owned. Clear both before loading the new
// org so sidebar/catalog consumers never render the previous authority's
// provider data during a switch; the store fences late responses as well.
watch(
  () => tenant.orgUUID,
  (org, previousOrg) => {
    if (org === previousOrg) return
    providers.resetForOrganization()
    if (auth.token) void providers.load(org)
  },
)

// Side-menu enabled set is per-workspace (it's derived from the
// APIBindings in the active workspace's kcp cluster). Without this
// watcher, refreshBindings only runs once at app boot — so switching
// to a workspace where a provider isn't enabled keeps showing the
// previous workspace's enabled chips in the sidebar, and switching
// to a workspace where MORE providers are enabled hides the new
// ones. Watch the tenant authority tuple as well as cluster readiness:
// organization-only mode intentionally has no clusterName, and two
// workspace rows can briefly share the same cluster readiness while the
// selected UUID changes. Best-effort: a 403 (workspace not bootstrapped yet)
// does not break the rest of the layout; refreshBindings surfaces a retry
// state to consumers.
watch(
  () => [tenant.orgUUID, tenant.workspaceUUID, tenant.workspaceMode, auth.clusterName] as const,
  () => {
    if (!providers.loaded) return
    providers.refreshBindings().catch(() => {
      /* failures already surface via missing Disable button / enable dialog */
    })
  },
)
</script>

<template>
  <router-view />
  <InlineNotification
    v-if="tenant.error && showTenantErrorInline"
    class="pointer-events-auto fixed inset-x-4 top-4 z-[2147483001] mx-auto max-w-[720px] shadow-xl"
    tone="error"
    title="Tenant operation failed"
    :message="tenant.error ?? ''"
    announce="assertive"
  />
  <ControlPlaneProvisioning v-if="showProvisioning" :attempts="tenant.bootstrapAttempts" />
  <!-- Persistent singleton: mounted above the router-view boundary so navigating
       between pages never unmounts the dock. AppLayout (which every page renders)
       lives *inside* router-view, so keeping the dock here is what preserves the
       live xterm buffer + SSH WebSocket across route changes. -->
  <TerminalDock v-show="!hideTerminalDock" />
  <PkConfirmDialog />
  <ToastHost owner="primary" />
</template>
