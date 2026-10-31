<script setup lang="ts">
import { ref } from 'vue'
import { RotateCcw } from 'lucide-vue-next'

import ResourceTable from '@/portalkit/ResourceTable.vue'
import { useAdminStore, type AdminOrg } from '@/stores/admin'

const admin = useAdminStore()

const workspaceColumns = [
  { key: 'displayName', label: 'Workspace', primary: true },
  { key: 'uuid', label: 'UUID' },
  { key: 'clusterName', label: 'Cluster' },
  { key: 'providers', label: 'Providers' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const workspaceRows = (org: AdminOrg): Array<Record<string, unknown>> => org.workspaces.map((workspace) => ({
  ...workspace,
  displayName: workspace.displayName || '—',
  clusterName: workspace.clusterName || '—',
  providers: workspace.providers ?? [],
}))

function providerNames(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((provider): provider is string => typeof provider === 'string')
    : []
}

function rowString(row: Record<string, unknown>, key: string): string {
  const value = row[key]
  return typeof value === 'string' ? value : ''
}

// purgeLabel renders the hub-reported purge deadline for a soft-deleted org
// or workspace. The hub computes it from its own grace period, so the portal
// never assumes a number of days.
function purgeLabel(purgeAt: string | undefined): string {
  if (!purgeAt) return ''
  const at = new Date(purgeAt)
  if (Number.isNaN(at.getTime())) return purgeAt
  return at.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

function restoreTitle(purgeAt: string | undefined): string {
  const deadline = purgeLabel(purgeAt)
  return deadline
    ? `Cancel the pending deletion. Recoverable until ${deadline}; after that the hub removes it for good.`
    : 'Cancel the pending deletion.'
}

const busy = ref(false)
// restoringKey names the org or org/workspace whose Restore is in flight so
// only that button shows the busy label.
const restoringKey = ref<string | null>(null)
const actionError = ref<string | null>(null)

async function refresh() {
  await admin.refresh()
}

async function runRestore(key: string, action: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  restoringKey.value = key
  actionError.value = null
  try {
    await action()
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    restoringKey.value = null
    busy.value = false
  }
}

function restoreOrg(org: AdminOrg) {
  return runRestore(org.name, () => admin.undeleteOrg(org.name))
}

function restoreWorkspace(org: AdminOrg, wsUUID: string) {
  return runRestore(`${org.name}/${wsUUID}`, () => admin.undeleteWorkspace(org.name, wsUUID))
}
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Organizations</h2>
    <p class="mb-4 text-sm text-text-muted">
      All organizations registered on the hub, with their workspaces, enabled providers and the
      metering account that pays for them. An organization or workspace marked <span class="k-badge k-badge--danger align-middle">deleting</span>
      is inside its recovery window and can be restored here until the hub purges it.
    </p>

    <p v-if="actionError" class="mb-2 text-sm text-danger" role="alert" aria-live="assertive">{{ actionError }}</p>

    <div v-if="admin.loading && !admin.loaded" class="text-sm text-text-muted" role="status" aria-live="polite">
      Loading organizations…
    </div>

    <div v-else-if="admin.loaded && !admin.orgs.length" class="text-sm text-text-muted" role="status" aria-live="polite">
      No organizations found.
    </div>

    <div v-if="admin.loaded" class="space-y-4">
      <div
        v-for="o in admin.orgs"
        :key="o.name"
        class="k-card p-4"
      >
        <div class="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1">
          <span class="text-sm font-semibold text-text-primary">{{ o.displayName || '—' }}</span>
          <span class="font-mono text-[11px] text-text-muted">{{ o.name }}</span>
          <span v-if="o.workspacePath" class="font-mono text-[11px] text-text-muted">
            {{ o.workspacePath }}
          </span>
          <span
            v-if="o.account"
            class="k-badge k-badge--muted normal-case"
            :title="o.ownAccount
              ? `Own metering account${o.plan ? ` on plan ${o.plan}` : ''}`
              : `Pooled into the metering account of ${o.createdBy || 'its creator'}; usage and limits are shared with their other organizations`"
          >
            {{ o.ownAccount ? (o.plan ? `plan ${o.plan}` : 'own account') : `account ${o.account}` }}
          </span>
          <template v-if="o.deletionRequestedAt">
            <span
              class="k-badge k-badge--danger"
              :title="o.purgeAt ? `Purged ${purgeLabel(o.purgeAt)}` : undefined"
            >
              deleting
            </span>
            <span v-if="o.purgeAt" class="text-[11px] text-text-muted">
              purged {{ purgeLabel(o.purgeAt) }}
            </span>
            <button
              type="button"
              class="k-btn k-btn--ghost ml-auto inline-flex items-center gap-1 px-2 py-0.5 text-xs text-accent disabled:opacity-50"
              :disabled="busy"
              :title="restoreTitle(o.purgeAt)"
              :aria-label="`Restore organization ${o.displayName || o.name}`"
              @click="restoreOrg(o)"
            >
              <RotateCcw class="h-3.5 w-3.5" :stroke-width="2" />
              {{ restoringKey === o.name ? 'Restoring…' : 'Restore' }}
            </button>
          </template>
        </div>

        <ResourceTable
          :columns="workspaceColumns"
          :rows="workspaceRows(o)"
          :aria-label="`Workspaces for ${o.displayName || o.name}`"
          row-key="uuid"
          variant="simple"
          :interactive="false"
          :loaded="admin.loaded"
          :loading="admin.loading"
          empty-text="No workspaces."
          @retry="refresh"
        >
          <template #displayName="{ row }">
            <span class="text-text-primary">{{ row.displayName || '—' }}</span>
            <span
              v-if="row.deletionRequestedAt"
              class="k-badge k-badge--danger ml-1"
              :title="rowString(row, 'purgeAt') ? `Purged ${purgeLabel(rowString(row, 'purgeAt'))}` : undefined"
            >
              deleting
            </span>
          </template>
          <template #uuid="{ value }">
            <span class="font-mono text-[11px] text-text-muted">{{ value || '—' }}</span>
          </template>
          <template #clusterName="{ value }">
            <span class="font-mono text-[11px] text-text-muted">{{ value || '—' }}</span>
          </template>
          <template #providers="{ value }">
            <div v-if="providerNames(value).length" class="flex flex-wrap gap-1">
              <span
                v-for="provider in providerNames(value)"
                :key="provider"
                class="k-badge k-badge--muted normal-case"
              >
                {{ provider }}
              </span>
            </div>
            <span v-else class="text-[11px] text-text-muted">—</span>
          </template>
          <template #actions="{ row }">
            <div v-if="row.deletionRequestedAt" class="flex items-center justify-end gap-2">
              <span v-if="rowString(row, 'purgeAt')" class="text-[11px] text-text-muted">
                purged {{ purgeLabel(rowString(row, 'purgeAt')) }}
              </span>
              <button
                type="button"
                class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2 py-0.5 text-xs text-accent disabled:opacity-50"
                :disabled="busy"
                :title="restoreTitle(rowString(row, 'purgeAt'))"
                :aria-label="`Restore workspace ${rowString(row, 'displayName') || rowString(row, 'uuid')}`"
                @click="restoreWorkspace(o, rowString(row, 'uuid'))"
              >
                <RotateCcw class="h-3.5 w-3.5" :stroke-width="2" />
                {{ restoringKey === `${o.name}/${rowString(row, 'uuid')}` ? 'Restoring…' : 'Restore' }}
              </button>
            </div>
          </template>
        </ResourceTable>
      </div>
    </div>
  </section>
</template>
