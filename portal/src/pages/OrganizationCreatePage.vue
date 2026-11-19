<!--
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowLeft,
  Hexagon,
  Loader2,
  Plus,
} from 'lucide-vue-next'
import { useTenantStore } from '@/stores/tenant'
import { trackEvent } from '@/telemetry/track'
import InlineNotification from '@/portalkit/InlineNotification.vue'

const tenant = useTenantStore()
const route = useRoute()
const router = useRouter()

const organizationName = ref('')
const creating = ref(false)
const localError = ref<string | null>(null)

// The chooser passes a validated internal path so this page can return to the
// user's original task after a cancel. Keep the validation here as well: the
// page can also be opened directly with a hand-edited query string.
function validatedInternalPath(value: unknown): string {
  const candidate = Array.isArray(value) ? value[0] : value
  if (typeof candidate !== 'string' || !candidate.startsWith('/') || candidate.startsWith('//')) {
    return '/'
  }

  try {
    const parsed = new URL(candidate, 'https://railgrid.internal')
    if (parsed.origin !== 'https://railgrid.internal') return '/'
    if (
      parsed.pathname === '/organizations' ||
      parsed.pathname.startsWith('/organizations/') ||
      parsed.pathname === '/login' ||
      parsed.pathname === '/auth/callback'
    ) {
      return '/'
    }
    return `${parsed.pathname}${parsed.search}${parsed.hash}` || '/'
  } catch {
    return '/'
  }
}

const backPath = computed(() => validatedInternalPath(route.query.from))
const chooserPath = computed(() => ({
  path: '/organizations',
  query: { from: backPath.value },
}))

function clearError() {
  localError.value = null
  tenant.clearError()
}

async function createOrganization() {
  if (creating.value) return
  const displayName = organizationName.value.trim()
  if (!displayName) return

  const submittingRoute = {
    name: route.name,
    fullPath: route.fullPath,
  }
  creating.value = true
  clearError()
  try {
    const created = await tenant.createOrg(displayName)
    if (!created) {
      localError.value = tenant.error ?? 'Failed to create organization.'
      trackEvent('organization_create_failed')
      return
    }
    if (
      route.name === submittingRoute.name &&
      route.fullPath === submittingRoute.fullPath
    ) {
      await router.replace(`/${created.uuid}/workspaces?preparing=1`)
    }
  } catch (error: unknown) {
    localError.value = error instanceof Error ? error.message : 'Failed to create organization.'
    trackEvent('organization_create_failed')
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <div class="relative flex min-h-screen w-full flex-col overflow-x-hidden bg-surface">
    <div class="contour-grid contour-grid-fade pointer-events-none absolute inset-x-0 top-0 h-72" aria-hidden="true" />

    <header class="relative z-10 mx-auto flex w-full max-w-4xl items-center justify-between px-5 py-5 sm:px-8">
      <router-link v-if="!creating" :to="chooserPath" class="k-btn k-back-action text-[13px]">
        <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        <span>Back</span>
      </router-link>
      <button v-else type="button" class="k-btn k-back-action text-[13px]" disabled aria-disabled="true">
        <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        <span>Back</span>
      </button>
      <div class="flex items-center gap-2 text-text-muted" aria-label="Railgrid">
        <span class="flex h-6 w-6 items-center justify-center rounded-md border border-border-subtle bg-surface-raised text-accent">
          <Hexagon class="h-3.5 w-3.5" :stroke-width="1.5" aria-hidden="true" />
        </span>
        <span class="type-display text-[12px] font-semibold tracking-[0.22em] text-text-primary">RAILGRID</span>
      </div>
    </header>

    <main class="relative z-10 flex flex-1 items-start justify-center px-4 py-8 sm:px-6 sm:py-12">
      <section class="k-create-page max-w-md" aria-labelledby="organization-create-title">
        <header class="k-create-header">
          <h1 id="organization-create-title" class="k-create-title">
            Create an organization
          </h1>
          <p class="k-create-description">An organization groups your workspaces and members.</p>
        </header>

        <form class="k-create-surface" :aria-busy="creating" @submit.prevent="createOrganization">
          <div class="k-create-body">
            <label for="organization-name" class="flex flex-col gap-1.5">
              <span class="text-xs font-semibold">
                Organization name
              </span>
              <input
                id="organization-name"
                v-model="organizationName"
                type="text"
                class="k-input w-full"
                placeholder="e.g. Acme"
                autocomplete="organization"
                autofocus
                :disabled="creating"
                :aria-describedby="localError ? 'organization-name-error' : undefined"
                @input="clearError"
              />
            </label>
            <InlineNotification v-if="localError" id="organization-name-error" tone="error" :message="localError" />
          </div>

          <div class="k-create-actions">
            <router-link v-if="!creating" :to="chooserPath" class="k-btn k-btn--ghost">Cancel</router-link>
            <button v-else type="button" class="k-btn k-btn--ghost" disabled aria-disabled="true">Cancel</button>
            <button
              type="submit"
              class="k-btn k-btn--primary"
              :disabled="creating || !organizationName.trim()"
            >
              <Loader2 v-if="creating" class="h-3.5 w-3.5 k-spin" :stroke-width="1.75" aria-hidden="true" />
              <Plus v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              {{ creating ? 'Creating…' : 'Create organization' }}
            </button>
          </div>
        </form>
      </section>
    </main>
  </div>
</template>
