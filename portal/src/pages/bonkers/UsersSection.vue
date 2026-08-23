<script setup lang="ts">
import { computed } from 'vue'

import ResourceTable from '@/portalkit/ResourceTable.vue'
import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()

const columns = [
  { key: 'email', label: 'Email' },
  { key: 'displayName', label: 'Name' },
  { key: 'rbacIdentity', label: 'RBAC identity' },
]

const userRows = computed<Record<string, unknown>[]>(() =>
  admin.users.map((user) => ({ ...user })),
)

async function refresh() {
  await admin.refresh()
}
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Users</h2>
    <p class="mb-4 text-sm text-text-muted">All users registered on the hub.</p>

    <ResourceTable
      :columns="columns"
      :rows="userRows"
      row-key="name"
      :interactive="false"
      searchable
      search-placeholder="Search users…"
      :search-keys="['name', 'email', 'displayName', 'rbacIdentity']"
      paginated
      :page-size="10"
      :loaded="admin.loaded"
      :loading="admin.loading"
      :error="admin.error"
      :stale="admin.loaded && !!admin.error"
      retryable
      empty-text="No users found."
      @retry="refresh"
    >
      <template #email="{ value }">
        <span class="text-text-primary">{{ value || '—' }}</span>
      </template>
      <template #displayName="{ value }">
        <span class="text-text-muted">{{ value || '—' }}</span>
      </template>
      <template #rbacIdentity="{ value }">
        <span class="font-mono text-[11px] text-text-muted">{{ value || '—' }}</span>
      </template>
    </ResourceTable>
  </section>
</template>
