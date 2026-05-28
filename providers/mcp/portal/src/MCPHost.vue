<script setup lang="ts">
// MCPHost is the root component the custom element mounts. It does
// three jobs:
//
//   1. Hydrates the provider-local auth store from kedgeContext (so
//      composables that read @/stores/auth get a valid token).
//   2. Keeps the internal router in sync with kedgeContext.subPath so
//      a portal navigation (browser back/forward, deep link refresh)
//      ends up at the correct internal route.
//   3. Renders <router-view /> — actual page rendering belongs to
//      MCPPage / MCPDetailPage, which stayed unchanged from Phase 2a.
//
// Bidirectional sync: the element class (element.ts) listens to the
// internal router's afterEach and emits kedge-navigate events the
// portal's ProviderFrame catches; this component listens to context
// changes coming back the other way.

import { onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore, type KedgeContext } from './auth-adapter'

const props = defineProps<{
  context: KedgeContext | null
  subPath: string
}>()

const auth = useAuthStore()
const router = useRouter()

// Hydrate auth on initial render and whenever the portal pushes a new
// context (theme toggle, token rotation, tenant switch).
watch(
  () => props.context,
  (ctx) => auth.hydrate(ctx),
  { immediate: true },
)

// React to portal-driven navigation: when subPath changes via the
// portal SPA (e.g. user pasted /providers/mcp/foo into the URL bar),
// reflect it on the internal router. Skip if we're already there to
// avoid loops with element.ts's afterEach handler.
watch(
  () => props.subPath,
  (sub) => {
    const want = '/' + (sub ?? '').replace(/^\//, '')
    if (router.currentRoute.value.path !== want) {
      router.replace(want)
    }
  },
)

onMounted(() => {
  // First-paint navigation already happened in createInternalRouter's
  // replace(initial), so we just need to confirm the router is alive.
  void router
})
</script>

<template>
  <router-view />
</template>
