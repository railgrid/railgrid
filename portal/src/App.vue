<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useProvidersStore } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useRouter } from 'vue-router'
import { registerProviderRoutes } from '@/router/providers'
import ControlPlaneProvisioning from '@/components/ControlPlaneProvisioning.vue'

const auth = useAuthStore()
const providers = useProvidersStore()
const tenant = useTenantStore()
const router = useRouter()

// Register the dynamic provider route shape exactly once at app boot, before
// any deep link like /providers/foo can be resolved.
registerProviderRoutes(router)

// First-login takeover: while the hub is still provisioning the user's
// personal org + first workspace, tenant.bootstrap() keeps bootstrapState
// at 'provisioning' and we cover the whole app with the "creating control
// plane" screen. Returning users (cached selection) flip straight to
// 'ready' inside bootstrap(), so this never flashes for them.
const showProvisioning = computed(
  () => auth.isAuthenticated && tenant.bootstrapState === 'provisioning',
)

onMounted(async () => {
  await auth.detectAuthMode()
  if (auth.isAuthenticated) {
    providers.load()
    tenant.bootstrap()
  }
})

// Authentication can arrive after onMounted (token login form). Watch and
// kick off provider load + tenant bootstrap as soon as credentials land.
watch(
  () => auth.isAuthenticated,
  (ok) => {
    if (!ok) return
    if (!providers.loaded) providers.load()
    tenant.bootstrap()
  },
)

// Tenant → auth bridge: the sidebar TenantContextChip switches the active
// org/workspace by mutating the tenant store, but every `/graphql/{cluster}`
// query is built from auth.clusterName. Without this sync the user flips
// workspace in the chip and the MCP/edges/workload pages keep showing data
// from the login-time DefaultCluster. Mirror activeWorkspace.clusterName →
// auth.clusterName so:
//   1. useGraphQLQuery's watchEffect (which reads auth.isAuthenticated, a
//      getter over s.clusterName) re-fires and re-queries the new cluster.
//   2. ProviderFrame's watch on auth.clusterName pushes a fresh
//      kedgeContext to the mounted provider element; its auth-adapter
//      hydrates and its useGraphQLQuery re-fires the same way.
// The hub omits clusterName until the workspace reports Ready, so guard on
// a non-empty value to avoid clearing a working selection during the brief
// window before fetchWorkspaces resolves on a hard refresh.
watch(
  () => tenant.activeWorkspace?.clusterName,
  (cluster) => {
    if (cluster) auth.setClusterName(cluster)
  },
)

// Side-menu enabled set is per-workspace (it's derived from the
// APIBindings in the active workspace's kcp cluster). Without this
// watcher, refreshBindings only runs once at app boot — so switching
// to a workspace where a provider isn't enabled keeps showing the
// previous workspace's enabled chips in the sidebar, and switching
// to a workspace where MORE providers are enabled hides the new
// ones. Refresh whenever the active cluster flips. Best-effort: a
// 403 (workspace not bootstrapped yet) doesn't break the rest of
// the layout, refreshBindings swallows it via load()'s catch path.
watch(
  () => auth.clusterName,
  (c) => {
    if (!c || !providers.loaded) return
    providers.refreshBindings().catch(() => {
      /* failures already surface via missing Disable button / enable dialog */
    })
  },
)
</script>

<template>
  <router-view />
  <ControlPlaneProvisioning v-if="showProvisioning" :attempts="tenant.bootstrapAttempts" />
</template>
