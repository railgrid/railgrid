<script setup lang="ts">
import { computed, ref } from 'vue'
import { Download, Plus } from 'lucide-vue-next'

import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { useAdminStore, type KubeconfigServer } from '@/stores/admin'

const admin = useAdminStore()

const columns = [
  { key: 'name', label: 'Name', primary: true, fullValue: (row: Record<string, unknown>) => providerDisplayName(row) },
  { key: 'status', label: 'Status' },
  { key: 'apiExportName', label: 'APIExport' },
  { key: 'workspaceCluster', label: 'Workspace cluster' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const providerRows = computed<Record<string, unknown>[]>(() =>
  admin.providers.map((provider) => ({
    ...provider,
    status: [
      provider.builtin ? 'core' : '',
      provider.onboarded ? 'provisioned' : '',
      provider.registered ? 'registered' : '',
      provider.registered ? (provider.ready ? 'ready' : 'not ready') : '',
    ].filter(Boolean).join(' '),
  })),
)

const newName = ref('')
const newDisplayName = ref('')
const busy = ref(false)
const deletingName = ref<string | null>(null)
const actionError = ref<string | null>(null)

function providerName(row: Record<string, unknown>): string {
  return String(row.name ?? '')
}

function providerDisplayName(row: Record<string, unknown>): string {
  const displayName = String(row.displayName ?? '').trim()
  return displayName || providerName(row)
}

function providerFlag(row: Record<string, unknown>, key: 'builtin' | 'onboarded' | 'registered' | 'ready'): boolean {
  return row[key] === true
}

async function refresh() {
  await admin.refresh()
}

async function create() {
  const name = newName.value.trim()
  if (!name) return
  busy.value = true
  actionError.value = null
  try {
    await admin.createProvider(name, newDisplayName.value.trim())
    newName.value = ''
    newDisplayName.value = ''
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

async function downloadKubeconfig(name: string, server?: KubeconfigServer) {
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
  if (!(await confirmDialog({ title: `Delete Provider "${name}"?`, message: 'This tears down its workspace (full teardown).', danger: true, confirmLabel: 'Delete' }))) return
  busy.value = true
  deletingName.value = name
  actionError.value = null
  try {
    await admin.deleteProvider(name)
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
    <h2 class="mb-1 text-base font-semibold text-text-primary">Providers</h2>
    <p class="mb-4 text-sm text-text-muted">
      Creating a <code>Provider</code> writes the object into
      <code>root:faros:system:providers</code>; the hub's Provider controller then provisions its
      workspace (<code>root:faros:providers:&lt;name&gt;</code>), ServiceAccount, and kubeconfig
      Secret. Deleting it triggers full teardown.
    </p>

    <div class="mb-4 flex flex-wrap items-end gap-2">
      <div>
        <label class="block text-[11px] text-text-muted">Name</label>
        <input
          v-model="newName"
          placeholder="e.g. code"
          class="k-input mt-1 w-48 font-mono text-sm"
          @keyup.enter="create"
        />
      </div>
      <div>
        <label class="block text-[11px] text-text-muted">Display name (optional)</label>
        <input
          v-model="newDisplayName"
          placeholder="e.g. Code"
          class="k-input mt-1 w-56 text-sm"
          @keyup.enter="create"
        />
      </div>
      <button
        type="button"
        class="k-btn k-btn--primary px-3 py-1.5 text-sm disabled:opacity-50"
        :disabled="busy || !newName.trim()"
        @click="create"
      >
        <Plus class="h-4 w-4" :stroke-width="1.75" />
        Create Provider
      </button>
    </div>
    <p v-if="actionError" class="mb-2 text-sm text-danger">{{ actionError }}</p>

    <ResourceTable
      :columns="columns"
      :rows="providerRows"
      aria-label="Providers"
      row-key="name"
      :interactive="false"
      searchable
      search-placeholder="Search providers…"
      :search-keys="['name', 'displayName', 'category', 'version', 'status', 'apiExportName', 'workspaceCluster']"
      paginated
      :page-size="10"
      :loaded="admin.loaded"
      :loading="admin.loading"
      :error="admin.error"
      :stale="admin.loaded && !!admin.error"
      retryable
      empty-text="No providers provisioned or registered."
      @retry="refresh"
    >
      <template #name="{ row }">
        <span v-if="providerDisplayName(row) !== providerName(row)" class="text-text-primary">
          {{ providerDisplayName(row) }}
        </span>
        <span class="block font-mono text-[12px] font-semibold text-text-primary">
          {{ providerName(row) }}
        </span>
      </template>
      <template #status="{ row }">
        <div class="flex flex-wrap gap-1">
          <StatusBadge v-if="providerFlag(row, 'builtin')" status="core" tone="muted" />
          <StatusBadge v-if="providerFlag(row, 'onboarded')" status="provisioned" tone="success" />
          <StatusBadge v-if="providerFlag(row, 'registered')" status="registered" tone="success" />
          <StatusBadge
            v-if="providerFlag(row, 'registered')"
            :status="providerFlag(row, 'ready') ? 'ready' : 'not ready'"
            :tone="providerFlag(row, 'ready') ? 'success' : 'danger'"
          />
          <span v-if="!providerFlag(row, 'builtin') && !providerFlag(row, 'onboarded') && !providerFlag(row, 'registered')" class="text-[11px] text-text-muted">—</span>
        </div>
      </template>
      <template #apiExportName="{ value }">
        <span class="font-mono text-[11px] text-text-muted">{{ value || '—' }}</span>
      </template>
      <template #workspaceCluster="{ value }">
        <span class="font-mono text-[11px] text-text-muted">{{ value || '(not provisioned)' }}</span>
      </template>
      <template #actions="{ row }">
        <div class="flex items-center justify-end gap-2">
          <!-- Builtins are bootstrapped by the hub; they have no Provider object. -->
          <span v-if="providerFlag(row, 'builtin')" class="text-[11px] text-text-muted">managed by hub</span>
          <template v-else>
            <span class="inline-flex items-center gap-1 text-xs">
              <Download class="h-3.5 w-3.5 text-text-muted" :stroke-width="2" />
              <template v-if="admin.kubeconfigServers.length > 1">
                <span class="text-text-muted">kubeconfig</span>
                <template v-for="(server, i) in admin.kubeconfigServers" :key="server">
                  <span v-if="i > 0" class="text-text-muted">·</span>
                  <button
                    type="button"
                    class="k-btn k-btn--ghost px-1 py-0.5 text-[11px] text-accent disabled:opacity-50"
                    :disabled="busy"
                    :title="serverTitles[server]"
                    @click="downloadKubeconfig(providerName(row), server)"
                  >
                    {{ serverLabels[server] }}
                  </button>
                </template>
              </template>
              <button
                v-else
                type="button"
                class="k-btn k-btn--ghost px-1 py-0.5 text-[11px] text-accent disabled:opacity-50"
                :disabled="busy"
                :title="admin.kubeconfigServers.length ? serverTitles[admin.kubeconfigServers[0]] : 'Download the minted provider kubeconfig'"
                @click="downloadKubeconfig(providerName(row), admin.kubeconfigServers[0])"
              >
                kubeconfig
              </button>
            </span>
            <ResourceTableDeleteButton
              :label="`Delete provider ${providerName(row)}`"
              :busy-label="`Deleting provider ${providerName(row)}…`"
              :busy="deletingName === providerName(row)"
              :disabled="busy"
              @click="remove(providerName(row))"
            />
          </template>
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
