<script setup lang="ts">
import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Organizations</h2>
    <p class="mb-4 text-sm text-text-muted">All organizations registered on the hub.</p>
    <table class="w-full text-sm">
      <thead class="text-left text-[11px] uppercase text-text-muted">
        <tr>
          <th class="py-1 pr-4">Display name</th>
          <th class="py-1 pr-4">UUID</th>
          <th class="py-1">Workspace path</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="o in admin.orgs" :key="o.name" class="border-t border-border-subtle/50">
          <td class="py-1.5 pr-4 text-text-primary">{{ o.displayName || '—' }}</td>
          <td class="py-1.5 pr-4 font-mono text-[11px] text-text-muted">{{ o.name }}</td>
          <td class="py-1.5 text-text-muted">{{ o.workspacePath || '—' }}</td>
        </tr>
        <tr v-if="!admin.orgs.length && !admin.loading">
          <td colspan="3" class="py-3 text-text-muted">No organizations found.</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
