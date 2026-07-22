<script setup lang="ts">
import { ref } from 'vue'
import { Plus, Trash2, Download } from 'lucide-vue-next'

import { useAdminStore } from '@/stores/admin'
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

async function downloadKubeconfig(name: string) {
  busy.value = true
  actionError.value = null
  try {
    await admin.downloadProviderKubeconfig(name)
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    busy.value = false
  }
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
      <code>root:kedge:system:providers</code>; the hub's Provider controller then provisions its
      workspace (<code>root:kedge:providers:&lt;name&gt;</code>), ServiceAccount, and kubeconfig
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
        class="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50"
        :disabled="busy || !newName.trim()"
        @click="create"
      >
        <Plus class="h-4 w-4" :stroke-width="2" />
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
              class="rounded-full border border-border-default/40 bg-surface-overlay/50 px-2 py-px text-[10px] text-text-muted"
            >core</span>
            <span
              v-if="p.onboarded"
              class="ml-1 rounded-full border border-success/30 bg-success-subtle px-2 py-px text-[10px] text-success"
            >provisioned</span>
            <span
              v-if="p.registered"
              class="ml-1 rounded-full border border-accent/30 bg-accent/10 px-2 py-px text-[10px] text-accent"
            >registered</span>
            <span v-if="!p.onboarded && !p.registered && !p.builtin" class="text-[11px] text-text-muted">—</span>
          </td>
          <td class="py-1.5 pr-4 text-text-muted">{{ p.apiExportName || '—' }}</td>
          <td class="py-1.5 pr-4 text-text-muted">{{ p.workspaceCluster || '(not provisioned)' }}</td>
          <td class="py-1.5 text-right">
            <!-- Builtins are bootstrapped by the hub; they have no Provider object. -->
            <span v-if="p.builtin" class="text-[11px] text-text-muted">managed by hub</span>
            <template v-else>
              <button
                class="mr-3 inline-flex items-center gap-1 text-xs text-accent disabled:opacity-50"
                :disabled="busy"
                title="Download the minted provider kubeconfig"
                @click="downloadKubeconfig(p.name)"
              >
                <Download class="h-3.5 w-3.5" :stroke-width="2" />
                kubeconfig
              </button>
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
