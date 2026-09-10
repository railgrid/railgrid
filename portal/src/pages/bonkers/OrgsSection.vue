<script setup lang="ts">
import ResourceTable from '@/portalkit/ResourceTable.vue'
import { useAdminStore, type AdminOrg } from '@/stores/admin'

const admin = useAdminStore()

const workspaceColumns = [
  { key: 'displayName', label: 'Workspace', primary: true },
  { key: 'uuid', label: 'UUID' },
  { key: 'clusterName', label: 'Cluster' },
  { key: 'providers', label: 'Providers' },
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

async function refresh() {
  await admin.refresh()
}
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Organizations</h2>
    <p class="mb-4 text-sm text-text-muted">
      All organizations registered on the hub, with their workspaces and enabled providers.
    </p>

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
        <div class="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span class="text-sm font-semibold text-text-primary">{{ o.displayName || '—' }}</span>
          <span class="font-mono text-[11px] text-text-muted">{{ o.name }}</span>
          <span v-if="o.workspacePath" class="font-mono text-[11px] text-text-muted">
            {{ o.workspacePath }}
          </span>
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
        </ResourceTable>
      </div>
    </div>
  </section>
</template>
