<script setup lang="ts">
// Root component for the kubernetes-edges custom element. Hydrates the
// provider-local Pinia auth store from the kedgeContext property the
// host portal sets on the element, and keeps the internal router in
// sync with portal-driven subPath changes (deep-link refresh, back/
// forward navigation in the portal SPA).
//
// Page rendering itself lives in EdgesPage / EdgeDetailPage /
// WorkloadsPage / WorkloadDetailPage; this host is purely the
// integration shim between the custom element and Vue's routing/
// state-management stacks.

import { watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore, type KedgeContext } from './auth-adapter'

const props = defineProps<{
  context: KedgeContext | null
  subPath: string
}>()

const auth = useAuthStore()
const router = useRouter()

watch(
  () => props.context,
  (ctx) => auth.hydrate(ctx),
  { immediate: true },
)

watch(
  () => props.subPath,
  (sub) => {
    const want = '/' + (sub ?? '').replace(/^\//, '')
    if (router.currentRoute.value.path !== want) {
      router.replace(want)
    }
  },
)
</script>

<template>
  <router-view />
</template>
