<script setup lang="ts">
import { ref } from 'vue'
import { Plus, Trash2, Download } from 'lucide-vue-next'

import { useAdminStore, type KubeconfigServer } from '@/stores/admin'
import { confirmDialog } from '@/portalkit/confirm'

const admin = useAdminStore()

const newName = ref('')
const newDisplayName = ref('')
const busy = ref(false)
const actionError = ref<string | null>(null)

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
  actionError.value = null
  try {
    await admin.deleteProvider(name)
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
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
          class="mt-1 w-48 rounded-lg border border-border-subtle bg-surface-raised/60 px-3 py-1.5 text-sm text-text-primary placeholder:text-text-muted focus:border-accent/40 focus:outline-none"
          @keyup.enter="create"
        />
      </div>
      <div>
        <label class="block text-[11px] text-text-muted">Display name (optional)</label>
        <input
          v-model="newDisplayName"
          placeholder="e.g. Code"
          class="mt-1 w-56 rounded-lg border border-border-subtle bg-surface-raised/60 px-3 py-1.5 text-sm text-text-primary placeholder:text-text-muted focus:border-accent/40 focus:outline-none"
          @keyup.enter="create"
        />
      </div>
      <button
        class="inline-flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white shadow-[0_0_16px_var(--color-accent-glow)] hover:bg-accent-hover disabled:opacity-50"
        :disabled="busy || !newName.trim()"
        @click="create"
      >
        <Plus class="h-4 w-4" :stroke-width="1.75" />
        Create Provider
      </button>
    </div>
    <p v-if="actionError" class="mb-2 text-sm text-danger">{{ actionError }}</p>

    <table class="w-full text-sm">
      <thead class="text-left text-[11px] uppercase text-text-muted">
        <tr>
          <th class="py-1 pr-4">Name</th>
          <th class="py-1 pr-4">Status</th>
          <th class="py-1 pr-4">APIExport</th>
          <th class="py-1 pr-4">Workspace cluster</th>
          <th class="py-1"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="p in admin.providers" :key="p.name" class="border-t border-border-subtle/50">
          <td class="py-1.5 pr-4 text-text-primary">{{ p.displayName || p.name }}</td>
          <td class="py-1.5 pr-4">
            <span
              v-if="p.builtin"
              class="rounded-sm border border-border-default/40 bg-surface-overlay/50 px-2 py-px text-[10px] text-text-muted"
            >core</span>
            <span
              v-if="p.onboarded"
              class="ml-1 rounded-sm border border-success/30 bg-success-subtle px-2 py-px text-[10px] text-success"
            >provisioned</span>
            <span
              v-if="p.registered"
              class="ml-1 rounded-sm border border-accent/30 bg-accent/10 px-2 py-px text-[10px] text-accent"
            >registered</span>
            <span v-if="!p.onboarded && !p.registered && !p.builtin" class="text-[11px] text-text-muted">—</span>
          </td>
          <td class="py-1.5 pr-4 text-text-muted">{{ p.apiExportName || '—' }}</td>
          <td class="py-1.5 pr-4 text-text-muted">{{ p.workspaceCluster || '(not provisioned)' }}</td>
          <td class="py-1.5 text-right">
            <!-- Builtins are bootstrapped by the hub; they have no Provider object. -->
            <span v-if="p.builtin" class="text-[11px] text-text-muted">managed by hub</span>
            <template v-else>
              <span class="mr-3 inline-flex items-center gap-1 text-xs">
                <Download class="h-3.5 w-3.5 text-text-muted" :stroke-width="2" />
                <template v-if="admin.kubeconfigServers.length > 1">
                  <span class="text-text-muted">kubeconfig</span>
                  <template v-for="(s, i) in admin.kubeconfigServers" :key="s">
                    <span v-if="i > 0" class="text-text-muted">·</span>
                    <button
                      class="text-accent disabled:opacity-50"
                      :disabled="busy"
                      :title="serverTitles[s]"
                      @click="downloadKubeconfig(p.name, s)"
                    >
                      {{ serverLabels[s] }}
                    </button>
                  </template>
                </template>
                <button
                  v-else
                  class="text-accent disabled:opacity-50"
                  :disabled="busy"
                  :title="
                    admin.kubeconfigServers.length
                      ? serverTitles[admin.kubeconfigServers[0]]
                      : 'Download the minted provider kubeconfig'
                  "
                  @click="downloadKubeconfig(p.name, admin.kubeconfigServers[0])"
                >
                  kubeconfig
                </button>
              </span>
              <button
                class="inline-flex items-center gap-1 text-xs text-danger disabled:opacity-50"
                :disabled="busy"
                @click="remove(p.name)"
              >
                <Trash2 class="h-3.5 w-3.5" :stroke-width="2" />
                Delete
              </button>
            </template>
          </td>
        </tr>
        <tr v-if="!admin.providers.length && !admin.loading">
          <td colspan="5" class="py-3 text-text-muted">No providers provisioned or registered.</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
