<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useProvidersStore } from '@/stores/providers'
import { useRouter } from 'vue-router'
import { registerProviderRoutes } from '@/router/providers'

const auth = useAuthStore()
const providers = useProvidersStore()
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
</script>

<template>
  <router-view />
</template>
