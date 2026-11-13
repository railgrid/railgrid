<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Download, Plus } from 'lucide-vue-next'

import ResourcePage from '@/portalkit/ResourcePage.vue'
import ResourceBackLink from '@/portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '@/portalkit/ResourceSectionCard.vue'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import ActionMenu from '@/portalkit/ActionMenu.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { useAdminStore, type KubeconfigServer } from '@/stores/admin'
import { changeTableQuery, tableState, type AdminTableChange } from './admin-relations'
import { systemWorkspaceNameError, systemWorkspaceStatus } from './platform-services'

// A system service is a platform service that is not a provider: it has no
// catalog entry and no Enable step, and reaches tenant workspaces through a
// workspace seed. This section is the provider onboarding flow at a different
// place in the workspace tree: create the workspace, hand its kubeconfig to
// the service's installer, then describe in a seed what every workspace gets.
const admin = useAdminStore()
const route = useRoute()
const router = useRouter()

const columns = [
  { key: 'name', label: 'Name', primary: true, fullValue: (row: Record<string, unknown>) => displayName(row) },
  { key: 'status', label: 'Status' },
  { key: 'workspacePath', label: 'Workspace' },
  { key: 'seedCount', label: 'Seeds' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const rows = computed<Record<string, unknown>[]>(() =>
  admin.systemWorkspaces.map(ws => ({ ...ws, status: systemWorkspaceStatus(ws).label, seedCount: ws.seeds.length })),
)
const state = computed(() => tableState(route.query, 'systemServices'))
function changeTable(change: AdminTableChange) { void router.replace({ query: changeTableQuery(route.query, 'systemServices', change) }) }

const selectedId = computed(() => typeof route.query.service === 'string' ? route.query.service : '')
const selected = computed(() => rows.value.find(row => row.name === selectedId.value))
const selectedSeeds = computed(() => (selected.value?.seeds as string[] | undefined) ?? [])
const listLocation = computed(() => {
  const query = { ...route.query }
  delete query.service
  return { path: route.path, query }
})
function serviceLocation(row: Record<string, unknown>) {
  return { path: route.path, query: { ...route.query, service: String(row.name) } }
}
function seedLocation(name: string) {
  return { path: '/bonkers/workspace-seeds', query: { seed: name } }
}
// Opens the seed editor with this service's workspace filled in as the path
// its exports are bound from.
const newSeedLocation = computed(() => ({
  path: '/bonkers/workspace-seeds',
  query: { newSeed: selectedId.value, exportPath: String(selected.value?.workspacePath ?? '') },
}))

const detail = ref<HTMLElement | null>(null)
let listTrigger: HTMLElement | null = null
function openService(row: Record<string, unknown>) {
  listTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push(serviceLocation(row))
}
function closeService() { void router.push(listLocation.value) }
watch([selectedId, () => admin.loaded], async ([name]) => {
  await nextTick()
  if (name) {
    const heading = detail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1')
    heading?.focus()
  } else if (listTrigger?.isConnected) listTrigger.focus()
}, { immediate: true })

const readError = computed(() => admin.error ? 'Couldn’t refresh system services. Try again.' : null)
const detailError = computed(() => readError.value || (admin.loaded && !selected.value
  ? 'This system service is no longer available. Return to System services to choose another.' : null))

const newName = ref('')
const newDisplayName = ref('')
const nameError = computed(() => systemWorkspaceNameError(newName.value.trim()))
const busy = ref(false)
const creating = ref(false)
const deletingName = ref<string | null>(null)
const actionError = ref<string | null>(null)
const notice = ref('')

function name(row: Record<string, unknown>): string { return String(row.name ?? '') }
function displayName(row: Record<string, unknown>): string {
  return String(row.displayName ?? '').trim() || name(row)
}
function status(row: Record<string, unknown>) { return systemWorkspaceStatus(row) }

async function refresh() { await admin.refresh() }

async function create() {
  const value = newName.value.trim()
  if (!value || nameError.value || busy.value) return
  busy.value = true
  creating.value = true
  notice.value = ''
  actionError.value = null
  try {
    await admin.createSystemWorkspace(value, newDisplayName.value.trim())
    notice.value = `System service “${value}” created. The hub is provisioning its workspace.`
    newName.value = ''
    newDisplayName.value = ''
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    creating.value = false
    busy.value = false
  }
}

const serverLabels: Record<KubeconfigServer, string> = { internal: 'in-cluster', external: 'external' }
const serverTitles: Record<KubeconfigServer, string> = {
  internal: 'Kubeconfig pointing at the hub’s in-cluster Service, for a service installed into this cluster.',
  external: 'Kubeconfig pointing at the hub’s public URL, for a service running outside this cluster.',
}
async function downloadKubeconfig(service: string, server?: KubeconfigServer) {
  if (busy.value) return
  busy.value = true
  actionError.value = null
  try {
    await admin.downloadSystemWorkspaceKubeconfig(service, server)
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// The hub refuses the delete while a seed references the workspace; saying so
// before the click saves a confirmation that could only fail.
function deleteBlocked(row: Record<string, unknown>): string {
  const seeds = (row.seeds as string[] | undefined) ?? []
  if (!seeds.length) return ''
  return `Referenced by ${seeds.length === 1 ? 'seed' : 'seeds'} ${seeds.join(', ')}. Remove the reference first.`
}

async function remove(service: string) {
  if (busy.value) return
  const confirmed = await confirmDialog({
    title: `Delete system service "${service}"?`,
    message: `This deletes the workspace root:railgrid:system:${service} with every APIExport and object in it. It cannot be undone.`,
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!confirmed) return
  busy.value = true
  deletingName.value = service
  actionError.value = null
  try {
    await admin.deleteSystemWorkspace(service)
    if (selectedId.value === service) await router.replace(listLocation.value)
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    deletingName.value = null
    busy.value = false
  }
}
</script>

<template>
  <section>
    <details v-if="admin.errorDetails" class="mb-4 text-xs text-text-muted"><summary class="cursor-pointer">Advanced error details</summary><pre class="mt-2 whitespace-pre-wrap break-all">{{ admin.errorDetails }}</pre></details>
    <p v-if="notice" class="mb-4 text-sm text-text-secondary" role="status">{{ notice }}</p>
    <div v-show="!selectedId">
      <h2 class="mb-1 text-base font-semibold text-text-primary">System services</h2>
      <p class="mb-4 max-w-3xl text-sm text-text-muted">
        Platform services that are not providers. Each gets a workspace under <span class="font-mono">root:railgrid:system</span> and a kubeconfig for its installer.
        It never appears in the catalog; a <router-link class="k-table-resource-link" to="/bonkers/workspace-seeds">workspace seed</router-link> puts it into every tenant workspace.
      </p>

      <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="create">
        <div>
          <label for="admin-system-service-name" class="block text-[11px] text-text-muted">Name</label>
          <input id="admin-system-service-name" v-model="newName" placeholder="e.g. switchboard" class="k-input mt-1 w-48 font-mono text-sm"
            required :aria-invalid="!!nameError" :aria-describedby="nameError ? 'admin-system-service-name-error' : undefined" />
        </div>
        <div>
          <label for="admin-system-service-display-name" class="block text-[11px] text-text-muted">Display name (optional)</label>
          <input id="admin-system-service-display-name" v-model="newDisplayName" placeholder="e.g. Switchboard" class="k-input mt-1 w-56 text-sm" />
        </div>
        <button type="submit" class="k-btn k-btn--primary px-3 py-1.5 text-sm disabled:opacity-50" :disabled="busy || !newName.trim() || !!nameError">
          <Plus class="h-4 w-4" :stroke-width="1.75" />
          {{ creating ? 'Creating system service…' : 'Create system service' }}
        </button>
      </form>
      <p v-if="nameError" id="admin-system-service-name-error" class="mb-2 text-sm text-danger">{{ nameError }}</p>
      <p v-if="actionError" class="mb-2 text-sm text-danger" role="alert" aria-live="assertive">{{ actionError }}</p>

      <ResourceTable v-bind="state" @change="changeTable"
        :columns="columns" :rows="rows" aria-label="System services" row-key="name"
        :interactive="true" :row-aria-label="(row: Record<string, unknown>) => `View system service ${displayName(row)}`" @row-click="openService"
        searchable search-placeholder="Search system services…" :search-keys="['name', 'displayName', 'status', 'workspacePath', 'workspaceCluster']"
        paginated :loaded="admin.loaded" :loading="admin.loading" :error="readError" :stale="admin.loaded && !!admin.error" retryable
        empty-text="No system services yet." @retry="refresh">
        <template #name="{ row }">
          <router-link class="k-table-resource-link" :to="serviceLocation(row)" @click.stop="listTrigger = $event.currentTarget as HTMLElement" @keydown.space.prevent.stop="openService(row)">{{ displayName(row) }}</router-link>
          <span v-if="displayName(row) !== name(row)" class="block font-mono text-xs text-text-muted">{{ name(row) }}</span>
        </template>
        <template #status="{ row }"><StatusBadge :status="status(row).label" :tone="status(row).tone" /></template>
        <template #workspacePath="{ value }"><span class="font-mono text-xs text-text-muted">{{ value }}</span></template>
        <template #seedCount="{ value }"><span class="text-text-secondary">{{ value || '—' }}</span></template>
        <template #actions="{ row }">
          <div class="flex items-center justify-end gap-2">
            <ResourceTableDeleteButton
              :label="deleteBlocked(row) ? `Delete system service ${name(row)} unavailable. ${deleteBlocked(row)}` : `Delete system service ${name(row)}`"
              :busy-label="`Deleting system service ${name(row)}…`" :busy="deletingName === name(row)"
              :disabled="busy || !!deleteBlocked(row)" @click="remove(name(row))" />
          </div>
        </template>
      </ResourceTable>
    </div>

    <div v-if="selectedId" ref="detail" class="space-y-4">
      <ResourceBackLink :href="router.resolve(listLocation).href" aria-label="Back to system services" @back="closeService">System services</ResourceBackLink>
      <ResourcePage :title="selected ? displayName(selected) : selectedId" kind="System service" compact-actions
        :loaded="!!selected" :loading="admin.loading" :error="detailError" :stale="!!selected && !!detailError" retryable @retry="refresh">
        <template v-if="selected" #status><StatusBadge :status="status(selected).label" :tone="status(selected).tone" /></template>
        <template v-if="selected" #meta><span class="font-mono">{{ selected.workspacePath }}</span></template>
        <template v-if="selected" #actions>
          <ActionMenu :label="`Actions for system service ${name(selected)}`" :busy="deletingName === selectedId" :busy-label="`Deleting system service ${selectedId}…`"
            :disabled="busy || admin.loading || !!detailError"
            :items="[{ id: 'delete', label: 'Delete system service', tone: 'danger', disabled: !!deleteBlocked(selected) }]"
            @select="id => id === 'delete' && selected && remove(name(selected))" />
        </template>
        <template v-if="selected">
          <p v-if="actionError" class="text-sm text-danger" role="alert">{{ actionError }}</p>
          <div class="space-y-4">
            <ResourceSectionCard id="system-service-workspace" title="Workspace" description="Provisioned by the hub from this system service.">
              <p v-if="!selected.ready && selected.message" class="mb-3 text-sm text-danger" role="status">{{ selected.message }}</p>
              <dl class="grid gap-4 text-sm sm:grid-cols-2">
                <div><dt class="text-text-muted">Workspace path</dt><dd class="break-all font-mono text-text-primary">{{ selected.workspacePath }}</dd></div>
                <div><dt class="text-text-muted">Workspace cluster</dt><dd class="break-all font-mono text-text-primary">{{ selected.workspaceCluster || 'Not provisioned' }}</dd></div>
                <div><dt class="text-text-muted">Name</dt><dd class="break-all font-mono text-text-primary">{{ selected.name }}</dd></div>
                <div><dt class="text-text-muted">Display name</dt><dd class="text-text-primary">{{ selected.displayName || '—' }}</dd></div>
              </dl>
            </ResourceSectionCard>

            <ResourceSectionCard id="system-service-access" title="Access credentials"
              description="Give this kubeconfig to the service’s installer. It is cluster-admin inside the service’s own workspace and has no access anywhere else.">
              <div class="flex flex-wrap gap-2">
                <template v-if="admin.kubeconfigServers.length">
                  <button v-for="server in admin.kubeconfigServers" :key="server" type="button" class="k-btn k-btn--ghost"
                    :disabled="busy || !selected.ready" :title="serverTitles[server]" @click="downloadKubeconfig(name(selected), server)">
                    <Download class="h-4 w-4" aria-hidden="true" />Download {{ serverLabels[server] }} kubeconfig
                  </button>
                </template>
                <button v-else type="button" class="k-btn k-btn--ghost" :disabled="busy || !selected.ready" @click="downloadKubeconfig(name(selected))">
                  <Download class="h-4 w-4" aria-hidden="true" />Download kubeconfig
                </button>
              </div>
              <p v-if="!selected.ready" class="mt-2 text-sm text-text-muted">Available once the workspace is ready.</p>
            </ResourceSectionCard>

            <ResourceSectionCard id="system-service-seeds" title="Workspace seeds"
              description="Seeds that bind this service’s exports into every tenant workspace. The service cannot be deleted while one references it.">
              <ul v-if="selectedSeeds.length" class="mb-3 space-y-1 text-sm">
                <li v-for="seed in selectedSeeds" :key="seed"><router-link class="k-table-resource-link font-mono" :to="seedLocation(seed)">{{ seed }}</router-link></li>
              </ul>
              <p v-else class="mb-3 text-sm text-text-secondary">No seed references this service yet, so no tenant workspace has it. Create one after the service’s installer has created its APIExports.</p>
              <router-link class="k-btn k-btn--ghost inline-flex items-center gap-1.5" :to="newSeedLocation">
                <Plus class="h-4 w-4" aria-hidden="true" />New seed for this service
              </router-link>
            </ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>
  </section>
</template>
