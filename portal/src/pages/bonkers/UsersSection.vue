<script setup lang="ts">
import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Users</h2>
    <p class="mb-4 text-sm text-text-muted">All users registered on the hub.</p>
    <table class="w-full text-sm">
      <thead class="text-left text-[11px] uppercase text-text-muted">
        <tr>
          <th class="py-1 pr-4">Email</th>
          <th class="py-1 pr-4">Name</th>
          <th class="py-1">RBAC identity</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in admin.users" :key="u.name" class="border-t border-border-subtle/50">
          <td class="py-1.5 pr-4 text-text-primary">{{ u.email }}</td>
          <td class="py-1.5 pr-4 text-text-muted">{{ u.displayName }}</td>
          <td class="py-1.5 font-mono text-[11px] text-text-muted">{{ u.rbacIdentity }}</td>
        </tr>
        <tr v-if="!admin.users.length && !admin.loading">
          <td colspan="3" class="py-3 text-text-muted">No users found.</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
