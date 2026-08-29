<script setup lang="ts">
import { computed } from 'vue'

import ResourceTable from '@/portalkit/ResourceTable.vue'
import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()

const columns = [
  { key: 'groupResource', label: 'Group / Resource', primary: true },
  { key: 'export', label: 'Export' },
  { key: 'identityHash', label: 'identityHash' },
]

const identityRows = computed<Record<string, unknown>[]>(() =>
  admin.identities.map((identity) => ({
    ...identity,
    groupResource: `${identity.resource}.${identity.group}`,
  })),
)

function identityRowKey(row: Record<string, unknown>): string {
  return [row.path, row.group, row.resource, row.export].map(value => String(value ?? '')).join('/')
}

async function refresh() {
  await admin.refresh()
}
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Root identities</h2>
    <p class="mb-4 text-sm text-text-muted">
      The <code>identityHash</code> for each first-party API. Copy the hash a provider needs (e.g.
      <code>edges.faros.sh</code> for kuery) into that provider's Helm values
      (<code>apiExport.edgesIdentityHash</code>).
    </p>

    <ResourceTable
      :columns="columns"
      :rows="identityRows"
      aria-label="Provider API identities"
      :row-key="identityRowKey"
      :interactive="false"
      searchable
      search-placeholder="Search root identities…"
      :search-keys="['groupResource', 'group', 'resource', 'export', 'identityHash', 'path']"
      paginated
      :page-size="10"
      :loaded="admin.loaded"
      :loading="admin.loading"
      :error="admin.error"
      :stale="admin.loaded && !!admin.error"
      retryable
      empty-text="No first-party identities found."
      @retry="refresh"
    >
      <template #groupResource="{ row }">
        <span class="font-mono text-[12px] text-text-primary">{{ row.groupResource }}</span>
      </template>
      <template #export="{ value }">
        <span class="font-mono text-[11px] text-text-muted">{{ value || '—' }}</span>
      </template>
      <template #identityHash="{ value }">
        <span class="font-mono text-[11px] text-text-muted">{{ value || '(not minted yet)' }}</span>
      </template>
    </ResourceTable>
  </section>
</template>
