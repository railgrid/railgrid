<script setup lang="ts">
// Root component for the server-edges custom element. Same wiring as
// the kubernetes-edges and mcp hosts — hydrate the provider-local
// auth store from kedgeContext and keep the internal router in sync
// with portal-driven subPath changes. See those for design notes.

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
