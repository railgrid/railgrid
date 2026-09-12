<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useRouteContextStore } from '@/stores/routeContext'
import { useAuthStore } from '@/stores/auth'
import { rememberPortalNext } from '@/auth/portalNext'
import { Loader2 } from 'lucide-vue-next'

const context = useRouteContextStore()
const router = useRouter()
const busy = computed(() => context.state === 'loading')
const title = computed(() => busy.value ? 'Opening destination' : context.state === 'pending' ? 'Workspace is provisioning' : 'Destination unavailable')
function retry() { if (context.target) void context.resolve(context.target, true) }
function switchAccount() {
  const destination = router.currentRoute.value.fullPath
  rememberPortalNext(destination)
  useAuthStore().logout()
  void router.push({ name: 'login', query: { switch: '1', returnTo: destination } })
}
</script>

<template>
  <main class="min-h-screen bg-surface px-6 py-16 text-text-primary">
    <section class="mx-auto max-w-lg" :aria-busy="busy" aria-live="polite">
      <Loader2 v-if="busy" class="mb-4 h-6 w-6 animate-spin text-accent motion-reduce:animate-none" aria-hidden="true" />
      <h1 class="text-xl font-semibold">{{ title }}</h1>
      <p class="mt-3 text-sm text-text-secondary">{{ busy ? 'Verifying your organization and workspace access…' : context.message }}</p>
      <div v-if="!busy" class="mt-6 flex flex-wrap gap-3">
        <button type="button" class="k-btn k-btn--primary" @click="retry">Retry</button>
        <button type="button" class="k-btn k-btn--ghost" @click="switchAccount">Switch account</button>
        <router-link to="/organizations" class="k-btn k-btn--ghost">Choose organization</router-link>
      </div>
    </section>
  </main>
</template>
