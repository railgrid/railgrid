<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useProvidersStore } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useRouter } from 'vue-router'
import { registerProviderRoutes } from '@/router/providers'

const auth = useAuthStore()
const providers = useProvidersStore()
const tenant = useTenantStore()
const router = useRouter()

// Register the dynamic provider route shape exactly once at app boot, before
// any deep link like /providers/foo can be resolved.
registerProviderRoutes(router)

onMounted(async () => {
  await auth.detectAuthMode()
  if (auth.isAuthenticated) {
    providers.load()
  }
})

// Authentication can arrive after onMounted (token login form). Watch and
// fetch the provider list as soon as credentials are available.
watch(
  () => auth.isAuthenticated,
  (ok) => {
    if (ok && !providers.loaded) providers.load()
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
</script>

<template>
  <router-view />
</template>
