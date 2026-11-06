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
import { bindingKey, changeTableQuery, tableContext, tableState, platformProvider, providerUsages, workspaceLocation, workspaceReferences, type AdminTableChange } from './admin-relations'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { useAdminStore, type KubeconfigServer } from '@/stores/admin'

const admin = useAdminStore()

const columns = [
  { key: 'name', label: 'Name', primary: true, fullValue: (row: Record<string, unknown>) => providerDisplayName(row) },
  { key: 'status', label: 'Status' },
  { key: 'category', label: 'Category' },
  { key: 'version', label: 'Version' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const providerRows = computed<Record<string, unknown>[]>(() =>
  admin.providers.map((provider) => ({
    ...provider,
    status: [
      provider.onboarded ? 'provisioned' : '',
      provider.registered ? 'registered' : '',
      provider.registered ? (provider.ready ? 'ready' : 'not ready') : '',
    ].filter(Boolean).join(' '),
  })),
)

const route = useRoute()
const router = useRouter()
const providersState = computed(() => tableState(route.query, 'providers'))
const providerWorkspacesState = computed(() => tableState(route.query, 'providerWorkspaces'))
function changeTable(prefix: string, change: AdminTableChange) { void router.replace({ query: changeTableQuery(route.query, prefix, change) }) }
const selectedId = computed(() => typeof route.query.provider === 'string' ? route.query.provider : '')
const originOrganization = computed(() => admin.orgs.find(org => org.name === route.query.originOrganization))
const originWorkspace = computed(() => originOrganization.value?.workspaces.find(ws => ws.uuid === route.query.originWorkspace))
const selectedReference = computed(() => originWorkspace.value && workspaceReferences(originWorkspace.value).find(ref => ref.name === selectedId.value && ref.scope === route.query.providerScope && ref.exportPath === route.query.exportPath))
const selectedProvider = computed<Record<string, unknown> | undefined>(() => {
  if (!route.query.providerScope) return providerRows.value.find(row => row.name === selectedId.value)
  const reference = selectedReference.value
  if (!reference) return undefined
  const provider = platformProvider(reference, admin.providers)
  return provider ? { ...provider } : { name: reference.name, displayName: reference.name, apiExportPath: reference.exportPath, referenceOnly: true }
})
const platformDetail = computed(() => !!selectedProvider.value && !selectedProvider.value.referenceOnly)
const selectedIdentity = computed(() => selectedReference.value || (selectedProvider.value ? { name: selectedId.value, scope: 'platform' as const, exportPath: String(selectedProvider.value.apiExportPath || '') } : null))
const usageRows = computed(() => selectedIdentity.value ? providerUsages(selectedIdentity.value, admin.orgs) : [])
const unavailableBindings = computed(() => admin.orgs.reduce((count, org) => count + org.workspaces.filter(ws => ws.providersUnavailable || (!ws.providerReferences && ws.providers.length > 0)).length, 0))
const usageColumns = [ { key: 'displayName', label: 'Workspace', primary: true }, { key: 'organization', label: 'Organization' }, { key: 'state', label: 'Binding state' } ]
function usageLocation(row: Record<string, unknown>) {
  const location = workspaceLocation(String(row.orgId), String(row.workspaceId))
  return { ...location, query: { ...tableContext(route.query), ...location.query, providerOriginOrganization: selectedReference.value ? route.query.originOrganization : undefined, providerOriginWorkspace: selectedReference.value ? route.query.originWorkspace : undefined, providerOriginView: route.query.originView, returnProvider: selectedId.value, returnProviderScope: selectedIdentity.value?.scope, returnExportPath: selectedIdentity.value?.exportPath } }
}
function openUsage(row: Record<string, unknown>) { void router.push(usageLocation(row)) }
const backLocation = computed(() => {
  if (!selectedReference.value || !originOrganization.value || !originWorkspace.value) return listLocation.value
  return { path: '/bonkers/organizations', query: { ...tableContext(route.query), organization: originOrganization.value.name,
    workspace: route.query.originView === 'organization' ? undefined : originWorkspace.value.uuid,
    bindingFocus: bindingKey(originWorkspace.value.uuid, selectedReference.value) } }
})
const backLabel = computed(() => selectedReference.value ? route.query.originView === 'organization' ? originOrganization.value?.displayName || 'Organization' : originWorkspace.value?.displayName || 'Workspace' : 'Providers')
const detail = ref<HTMLElement | null>(null)
let listTrigger: HTMLElement | null = null
const listLocation = computed(() => {
  const query = { ...route.query }
  delete query.provider
  delete query.originView
  delete query.usageFocus
  delete query.providerScope
  delete query.exportPath
  delete query.originOrganization
  delete query.originWorkspace
  return { path: route.path, query }
})
function providerLocation(row: Record<string, unknown>) {
  return { path: route.path, query: { ...route.query, provider: String(row.name) } }
}
function openProvider(row: Record<string, unknown>) {
  listTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push(providerLocation(row))
}
function closeProvider() { void router.push(backLocation.value) }
watch([selectedId, () => admin.loaded], async ([name]) => {
  await nextTick()
  if (name) {
    const rowLink = Array.from(detail.value?.querySelectorAll<HTMLElement>('[data-usage-id]') || []).find(el => el.dataset.usageId === route.query.usageFocus && el.getClientRects().length)
    if (rowLink) { rowLink.focus(); return }
    const heading = detail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1')
    heading?.focus()
  } else if (listTrigger?.isConnected) listTrigger.focus()
}, { immediate: true })
const readError = computed(() => admin.error ? 'Couldn’t refresh providers. Try again.' : null)
const detailError = computed(() => readError.value || (admin.loaded && !selectedProvider.value
  ? 'This provider is no longer available. Return to Providers to choose another.' : null))

const newName = ref('')
const newDisplayName = ref('')
const busy = ref(false)
const deletingName = ref<string | null>(null)
const actionError = ref<string | null>(null)
const notice = ref('')
const creating = ref(false)

function providerName(row: Record<string, unknown>): string {
  return String(row.name ?? '')
}

function providerDisplayName(row: Record<string, unknown>): string {
  const displayName = String(row.displayName ?? '').trim()
  return displayName || providerName(row)
}

function providerFlag(row: Record<string, unknown>, key: 'onboarded' | 'registered' | 'ready'): boolean {
  return row[key] === true
}

function providerStatus(row: Record<string, unknown>): { label: string; tone: 'success' | 'warning' | 'danger' | 'muted' } {
  if (row.referenceOnly) return { label: 'Not reported here', tone: 'muted' }
  if (providerFlag(row, 'registered')) return providerFlag(row, 'ready')
    ? { label: 'Ready', tone: 'success' }
    : { label: 'Not ready', tone: 'danger' }
  if (providerFlag(row, 'onboarded')) return { label: 'Awaiting registration', tone: 'warning' }
  return { label: 'Not provisioned', tone: 'muted' }
}

async function refresh() {
  await admin.refresh()
}

async function create() {
  const name = newName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  creating.value = true
  notice.value = ''
  actionError.value = null
  try {
    await admin.createProvider(name, newDisplayName.value.trim())
    notice.value = `Provider “${name}” created. Waiting for provisioning and registration.`
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

async function downloadKubeconfig(name: string, server?: KubeconfigServer) {
  if (busy.value) return
  busy.value = true
  actionError.value = null
  try {
    await admin.downloadProviderKubeconfig(name, server)
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// One download button per address the hub can serve. Labelled by where the
// provider runs rather than by the flag name — that's the question the admin can
// actually answer. A hub without --hub-internal-url offers only 'external',
// which collapses to the single plain "kubeconfig" button this table had before.
const serverLabels: Record<KubeconfigServer, string> = {
  internal: 'in-cluster',
  external: 'external',
}
const serverTitles: Record<KubeconfigServer, string> = {
  internal:
    'Kubeconfig pointing at the hub’s in-cluster Service — for a provider installed by Helm into this cluster. Keeps its traffic off the public path.',
  external:
    'Kubeconfig pointing at the hub’s public URL — for a provider running outside this cluster.',
}

async function remove(name: string) {
  if (busy.value) return
  if (!(await confirmDialog({ title: `Delete Provider "${name}"?`, message: 'This tears down its workspace (full teardown).', danger: true, confirmLabel: 'Delete' }))) return
  busy.value = true
  deletingName.value = name
  actionError.value = null
  try {
    await admin.deleteProvider(name)
    if (selectedId.value === name) await router.replace(listLocation.value)
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
    <h2 class="mb-1 text-base font-semibold text-text-primary">Providers</h2>
    <p class="mb-4 text-sm text-text-muted">
      Manage provider registration, readiness, and access credentials. Deleting a provider tears down its workspace.
    </p>

    <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="create">
      <div>
        <label for="admin-provider-name" class="block text-[11px] text-text-muted">Name</label>
        <input
          id="admin-provider-name"
          v-model="newName"
          placeholder="e.g. code"
          class="k-input mt-1 w-48 font-mono text-sm"
          required
        />
      </div>
      <div>
        <label for="admin-provider-display-name" class="block text-[11px] text-text-muted">Display name (optional)</label>
        <input
          id="admin-provider-display-name"
          v-model="newDisplayName"
          placeholder="e.g. Code"
          class="k-input mt-1 w-56 text-sm"
        />
      </div>
      <button
        type="submit"
        class="k-btn k-btn--primary px-3 py-1.5 text-sm disabled:opacity-50"
        :disabled="busy || !newName.trim()"
      >
        <Plus class="h-4 w-4" :stroke-width="1.75" />
        {{ creating ? 'Creating provider…' : 'Create provider' }}
      </button>
    </form>
    <p v-if="actionError" class="mb-2 text-sm text-danger" role="alert" aria-live="assertive">{{ actionError }}</p>

    <ResourceTable v-bind="providersState" @change="changeTable('providers', $event)"
      :columns="columns"
      :rows="providerRows"
      aria-label="Providers"
      row-key="name"
      :interactive="true"
      :row-aria-label="(row: Record<string, unknown>) => `View provider ${providerDisplayName(row)}`"
      @row-click="openProvider"
      searchable
      search-placeholder="Search providers…"
      :search-keys="['name', 'displayName', 'category', 'version', 'status', 'apiExportName', 'workspaceCluster']"
      paginated
      :loaded="admin.loaded"
      :loading="admin.loading"
      :error="readError"
      :stale="admin.loaded && !!admin.error"
      retryable
      empty-text="No providers provisioned or registered."
      @retry="refresh"
    >
      <template #name="{ row }">
        <router-link class="k-table-resource-link" :to="providerLocation(row)" @click.stop="listTrigger = $event.currentTarget as HTMLElement" @keydown.space.prevent.stop="openProvider(row)">{{ providerDisplayName(row) }}</router-link>
        <span v-if="providerDisplayName(row) !== providerName(row)" class="block font-mono text-xs text-text-muted">{{ providerName(row) }}</span>
      </template>
      <template #status="{ row }">
        <StatusBadge :status="providerStatus(row).label" :tone="providerStatus(row).tone" />
      </template>
      <template #category="{ value }"><span class="text-text-secondary">{{ value || '—' }}</span></template>
      <template #version="{ value }"><span class="font-mono text-xs text-text-muted">{{ value || '—' }}</span></template>
      <template #actions="{ row }">
        <div class="flex items-center justify-end gap-2">
          <ResourceTableDeleteButton
            :label="`Delete provider ${providerName(row)}`"
            :busy-label="`Deleting provider ${providerName(row)}…`"
            :busy="deletingName === providerName(row)"
            :disabled="busy"
            @click="remove(providerName(row))"
          />
        </div>
      </template>
    </ResourceTable>
    </div>
    <div v-if="selectedId" ref="detail" class="space-y-4">
      <ResourceBackLink :href="router.resolve(backLocation).href" :aria-label="selectedReference ? route.query.originView === 'organization' ? 'Back to organization' : 'Back to workspace' : 'Back to providers'" @back="closeProvider">{{ backLabel }}</ResourceBackLink>
      <ResourcePage :title="selectedProvider ? providerDisplayName(selectedProvider) : selectedId" kind="Provider" compact-actions
        :loaded="!!selectedProvider" :loading="admin.loading" :error="detailError" :stale="!!selectedProvider && !!detailError" retryable @retry="refresh">
        <template v-if="selectedProvider" #status><StatusBadge :status="providerStatus(selectedProvider).label" :tone="providerStatus(selectedProvider).tone" /></template>
        <template v-if="selectedProvider" #meta><span>{{ selectedReference?.scope === 'organization' ? 'Organization-owned provider' : selectedProvider.category || 'Platform provider' }}</span><span v-if="selectedProvider.version">{{ selectedProvider.version }}</span></template>
        <template v-if="platformDetail && selectedProvider" #actions>
          <ActionMenu :label="`Actions for provider ${providerName(selectedProvider)}`" :busy="deletingName === selectedId" :busy-label="`Deleting provider ${selectedId}…`" :disabled="busy || admin.loading || !!detailError"
            :items="[{ id: 'delete', label: 'Delete provider', tone: 'danger' }]" @select="id => id === 'delete' && selectedProvider && remove(providerName(selectedProvider))" />
        </template>
        <template v-if="selectedProvider">
          <p v-if="actionError" class="text-sm text-danger" role="alert">{{ actionError }}</p>
          <div class="space-y-4">
          <p v-if="selectedProvider.referenceOnly" class="text-sm text-text-secondary">This binding belongs to {{ selectedReference?.scope === 'organization' ? 'an organization-owned provider' : 'a provider without a platform registration' }}. Platform readiness and access credentials are unavailable here.</p>
          <ResourceSectionCard v-if="platformDetail" id="provider-registration" title="Registration" description="Provisioning and registration reported by the hub.">
          <dl class="grid gap-4 text-sm sm:grid-cols-2">
            <div>
              <dt class="text-text-muted">Provisioned</dt>
              <dd class="text-text-primary">{{ providerFlag(selectedProvider, 'onboarded') ? 'Yes' : 'No' }}</dd>
            </div>
            <div>
              <dt class="text-text-muted">Registered</dt>
              <dd class="text-text-primary">{{ providerFlag(selectedProvider, 'registered') ? 'Yes' : 'No' }}</dd>
            </div>
            <div>
              <dt class="text-text-muted">APIExport</dt>
              <dd class="break-all font-mono text-text-primary">{{ selectedProvider.apiExportName || '—' }}</dd>
            </div>
            <div>
              <dt class="text-text-muted">Workspace cluster</dt>
              <dd class="break-all font-mono text-text-primary">{{ selectedProvider.workspaceCluster || 'Not provisioned' }}</dd>
            </div>
          </dl>
          </ResourceSectionCard>
          <ResourceSectionCard v-if="platformDetail" id="provider-access" title="Access credentials" description="Download a kubeconfig for where this provider runs.">
            <div class="flex flex-wrap gap-2">
              <template v-if="admin.kubeconfigServers.length">
                <button v-for="server in admin.kubeconfigServers" :key="server" type="button" class="k-btn k-btn--ghost"
                  :disabled="busy" :title="serverTitles[server]" @click="downloadKubeconfig(providerName(selectedProvider), server)">
                  <Download class="h-4 w-4" aria-hidden="true" />Download {{ serverLabels[server] }} kubeconfig
                </button>
              </template>
              <button v-else type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="downloadKubeconfig(providerName(selectedProvider))">
                <Download class="h-4 w-4" aria-hidden="true" />Download kubeconfig
              </button>
            </div>

          </ResourceSectionCard>
          <ResourceSectionCard id="provider-workspaces" title="Enabled in" description="Workspaces with a binding to this provider. Binding state is independent of provider readiness.">
            <p v-if="unavailableBindings" class="mb-3 text-sm text-text-secondary" role="status">Bindings for {{ unavailableBindings }} {{ unavailableBindings === 1 ? 'workspace are' : 'workspaces are' }} unavailable. This list may be incomplete; refresh to try again.</p>
            <ResourceTable v-bind="providerWorkspacesState" @change="changeTable('providerWorkspaces', $event)" :columns="usageColumns" :rows="usageRows" aria-label="Provider workspaces" row-key="name" searchable search-placeholder="Search workspaces…" :search-keys="['displayName', 'organization', 'state']" paginated :loaded="admin.loaded" :loading="admin.loading" :interactive="true" :row-aria-label="(row: Record<string, unknown>) => `View workspace ${row.displayName} in ${row.organization}`" @row-click="openUsage" empty-text="No verified workspace bindings were found for this provider.">
              <template #displayName="{ row }"><router-link class="k-table-resource-link" :to="usageLocation(row)" :data-usage-id="row.name" @click.stop>{{ row.displayName }}</router-link></template>
              <template #state="{ row }"><StatusBadge :status="String(row.state)" :tone="row.state === 'Disabling' ? 'warning' : 'muted'" /></template>
            </ResourceTable>
          </ResourceSectionCard>
          <ResourceSectionCard id="provider-identity" title="Identity">
            <dl class="grid gap-4 text-sm sm:grid-cols-2">
              <div><dt class="text-text-muted">Provider ID</dt><dd class="break-all font-mono text-text-primary">{{ selectedProvider.name }}</dd></div>
              <div v-if="selectedReference"><dt class="text-text-muted">Export workspace</dt><dd class="break-all font-mono text-text-primary">{{ selectedReference.exportPath || 'Unavailable' }}</dd></div>
              <div><dt class="text-text-muted">Display name</dt><dd class="text-text-primary">{{ selectedProvider.displayName || '—' }}</dd></div>
            </dl>
          </ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>
  </section>
</template>
