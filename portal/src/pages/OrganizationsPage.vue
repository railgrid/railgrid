<!--
Copyright 2026 The Faros Authors.

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
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  AlertCircle,
  ArrowLeft,
  Building2,
  Check,
  Hexagon,
  Loader2,
  Plus,
  RefreshCw,
  UserRound,
} from 'lucide-vue-next'
import { useTenantStore, type OrgRow } from '@/stores/tenant'

const tenant = useTenantStore()
const route = useRoute()
const router = useRouter()

const loading = ref(false)
const loaded = ref(false)
const switchingOrg = ref<string | null>(null)
const failedSwitchOrg = ref<string | null>(null)
const localError = ref<string | null>(null)
let organizationsLoadGeneration = 0

// The access menu passes the page the user came from so the chooser can be a
// reversible decision. Only same-origin absolute paths are accepted; this also
// prevents a stale or hand-edited query value from becoming an open redirect.
// The chooser itself is never a valid return target.
function validatedInternalPath(value: unknown): string {
  const candidate = Array.isArray(value) ? value[0] : value
  if (typeof candidate !== 'string' || !candidate.startsWith('/') || candidate.startsWith('//')) {
    return '/settings/workspaces'
  }

  try {
    const parsed = new URL(candidate, 'https://faros.internal')
    if (parsed.origin !== 'https://faros.internal') return '/settings/workspaces'
    if (
      parsed.pathname === '/organizations' ||
      parsed.pathname.startsWith('/organizations/') ||
      parsed.pathname === '/login' ||
      parsed.pathname === '/auth/callback'
    ) {
      return '/settings/workspaces'
    }
    return `${parsed.pathname}${parsed.search}${parsed.hash}` || '/settings/workspaces'
  } catch {
    return '/settings/workspaces'
  }
}

const backPath = computed(() => validatedInternalPath(route.query.from))
const pageError = computed(() => localError.value)

async function loadOrganizations() {
  const requestGeneration = ++organizationsLoadGeneration
  loading.value = true
  failedSwitchOrg.value = null
  localError.value = null
  try {
    await tenant.fetchOrgs()
    if (requestGeneration !== organizationsLoadGeneration) return
    if (tenant.error) localError.value = tenant.error
  } catch (error: unknown) {
    if (requestGeneration === organizationsLoadGeneration) {
      localError.value = error instanceof Error ? error.message : 'Failed to load organizations.'
    }
  } finally {
    if (requestGeneration === organizationsLoadGeneration) {
      loaded.value = true
      loading.value = false
    }
  }
}

function organizationType(org: OrgRow): string {
  return org.personal ? 'Personal organization' : 'Shared organization'
}

function organizationRole(org: OrgRow): string {
  return org.role === 'admin' ? 'Admin access' : 'Member access'
}

function createdLabel(org: OrgRow): string {
  if (!org.createdAt) return ''
  const date = new Date(org.createdAt)
  if (Number.isNaN(date.valueOf())) return ''
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date)
}

async function chooseOrganization(org: OrgRow) {
  if (switchingOrg.value) return
  // The chooser can be reached even when the requested organization is
  // already active (for example from a stale account-menu link). Continuing
  // must still leave the chooser; otherwise the current organization row is
  // a dead end for keyboard and pointer users.
  if (org.uuid === tenant.orgUUID) {
    localError.value = null
    failedSwitchOrg.value = null
    try {
      await router.replace(backPath.value)
    } catch (error: unknown) {
      localError.value = error instanceof Error ? error.message : 'Failed to continue.'
    }
    return
  }

  switchingOrg.value = org.uuid
  localError.value = null
  try {
    tenant.clearError()
    // This action deliberately clears workspaceUUID and does not select a
    // default. Workspace selection remains an explicit, separate decision.
    await tenant.selectOrganization(org.uuid)
    if (tenant.error) {
      // selectOrganization reports workspace-list failures on the store and
      // resolves so callers can decide how to recover. Do not navigate to
      // settings while the new org's workspace state is unknown.
      failedSwitchOrg.value = org.uuid
      localError.value = tenant.error
      return
    }
    failedSwitchOrg.value = null
    await router.replace('/settings/workspaces')
  } catch (error: unknown) {
    failedSwitchOrg.value = org.uuid
    localError.value = error instanceof Error ? error.message : 'Failed to switch organization.'
  } finally {
    switchingOrg.value = null
  }
}

async function retryFailedSwitch() {
  const orgUUID = failedSwitchOrg.value
  if (!orgUUID || switchingOrg.value) return
  switchingOrg.value = orgUUID
  localError.value = null
  try {
    tenant.clearError()
    await tenant.fetchWorkspaces(orgUUID, { selectDefault: false })
    if (tenant.error) {
      localError.value = tenant.error
      return
    }
    failedSwitchOrg.value = null
    await router.replace('/settings/workspaces')
  } catch (error: unknown) {
    localError.value = error instanceof Error ? error.message : 'Failed to load workspaces.'
  } finally {
    switchingOrg.value = null
  }
}

function retry() {
  if (failedSwitchOrg.value) void retryFailedSwitch()
  else void loadOrganizations()
}

onMounted(() => { void loadOrganizations() })
</script>

<template>
  <div class="relative flex min-h-screen w-full flex-col overflow-x-hidden bg-surface">
    <div class="contour-grid contour-grid-fade pointer-events-none absolute inset-x-0 top-0 h-72" aria-hidden="true" />

    <header class="relative z-10 mx-auto flex w-full max-w-4xl items-center justify-between px-5 py-5 sm:px-8">
      <router-link :to="backPath" class="k-btn k-back-action text-[13px]">
        <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        <span>Back</span>
      </router-link>
      <div class="flex items-center gap-2 text-text-muted" aria-label="Faros">
        <span class="flex h-6 w-6 items-center justify-center rounded-md border border-border-subtle bg-surface-raised text-accent">
          <Hexagon class="h-3.5 w-3.5" :stroke-width="1.5" aria-hidden="true" />
        </span>
        <span class="type-display text-[12px] font-semibold tracking-[0.22em] text-text-primary">FAROS</span>
      </div>
    </header>

    <main class="relative z-10 flex flex-1 items-start justify-center px-4 py-8 sm:px-6 sm:py-12">
      <section class="w-full max-w-2xl" aria-labelledby="organization-chooser-title">
        <div class="mb-5">
          <div class="min-w-0">
            <h1 id="organization-chooser-title" class="type-display mt-2 text-[24px] font-semibold tracking-tight text-text-primary">
              Choose an organization to continue
            </h1>
          </div>
        </div>

        <div
          v-if="pageError && tenant.orgs.length > 0"
          role="alert"
          class="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[11px] text-danger"
        >
          <span class="flex min-w-0 items-start gap-2">
            <AlertCircle class="mt-px h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span>{{ pageError }}</span>
          </span>
          <button type="button" class="k-btn k-btn--text shrink-0 text-[10px]" @click="retry">
            <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
            Retry
          </button>
        </div>

        <div class="k-card overflow-hidden">
            <div class="border-b border-border-subtle px-3 py-3">
              <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <h2 class="text-[13px] font-semibold text-text-primary">Your organizations</h2>
                </div>
                <span v-if="loading && tenant.orgs.length > 0" class="flex items-center gap-1.5 text-[10px] text-text-muted" role="status">
                  <Loader2 class="h-3 w-3 animate-spin" :stroke-width="1.75" aria-hidden="true" />
                  Refreshing
                </span>
              </div>
            </div>

            <div v-if="(!loaded || loading) && tenant.orgs.length === 0" class="flex min-h-52 flex-col items-center justify-center gap-2 px-4 py-8 text-center text-[11px] text-text-muted" role="status">
              <Loader2 class="h-5 w-5 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
              <span>Loading organizations…</span>
            </div>

            <div v-else-if="pageError && tenant.orgs.length === 0" class="flex min-h-52 flex-col items-center justify-center gap-3 px-4 py-8 text-center" role="alert">
              <AlertCircle class="h-5 w-5 text-danger" :stroke-width="1.75" aria-hidden="true" />
              <p class="max-w-sm text-[11px] text-danger">{{ pageError }}</p>
              <button type="button" class="k-btn k-btn--ghost text-[11px]" @click="retry">
                <RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                Retry
              </button>
            </div>

            <div v-else-if="loaded && tenant.orgs.length === 0" class="flex min-h-52 flex-col items-center justify-center gap-2 px-4 py-8 text-center">
              <Building2 class="h-5 w-5 text-text-muted" :stroke-width="1.5" aria-hidden="true" />
              <p class="text-[12px] font-medium text-text-primary">No organizations yet</p>
              <p class="max-w-sm text-[11px] text-text-muted">Create an organization to get started.</p>
              <router-link :to="{ path: '/organizations/new', query: { from: backPath } }" class="k-btn k-btn--primary mt-2 text-[11px]">
                <Plus class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                Create organization
              </router-link>
            </div>

            <ul v-else class="p-1" aria-label="Organizations">
              <li v-for="org in tenant.orgs" :key="org.uuid">
                <button
                  type="button"
                  class="k-menu-item min-h-[68px] w-full py-2.5"
                  :class="[
                    tenant.orgUUID === org.uuid ? 'is-selected' : '',
                    switchingOrg && switchingOrg !== org.uuid ? 'opacity-60' : '',
                  ]"
                  :aria-pressed="tenant.orgUUID === org.uuid"
                  :disabled="switchingOrg !== null"
                  @click="chooseOrganization(org)"
                >
                <span class="flex h-8 w-8 shrink-0 items-center justify-center text-accent">
                  <UserRound v-if="org.personal" class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
                  <Building2 v-else class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
                </span>
                <span class="min-w-0 flex-1 text-left">
                  <span class="flex min-w-0 items-center gap-2">
                    <span class="truncate font-mono text-[12px] text-text-primary">{{ org.displayName || 'Unnamed organization' }}</span>
                  </span>
                  <span class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-text-muted">
                    <span>{{ organizationType(org) }}</span>
                    <span aria-hidden="true">·</span>
                    <span>{{ organizationRole(org) }}</span>
                    <template v-if="createdLabel(org)">
                      <span aria-hidden="true">·</span>
                      <span>Created {{ createdLabel(org) }}</span>
                    </template>
                  </span>
                </span>
                <span class="flex shrink-0 items-center gap-2">
                  <Loader2 v-if="switchingOrg === org.uuid" class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" aria-label="Switching organization" />
                  <Check v-else-if="tenant.orgUUID === org.uuid" class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="2" aria-hidden="true" />
                </span>
                </button>
              </li>
            </ul>

            <div v-if="loaded && tenant.orgs.length > 0" class="flex justify-end border-t border-border-subtle px-3 py-3">
              <router-link :to="{ path: '/organizations/new', query: { from: backPath } }" class="k-btn k-btn--primary text-[11px]">
                <Plus class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                Create organization
              </router-link>
            </div>
            </div>
          </section>
    </main>
  </div>
</template>
