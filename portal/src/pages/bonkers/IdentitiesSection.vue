<script setup lang="ts">
import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Root identities</h2>
    <p class="mb-4 text-sm text-text-muted">
      The <code>identityHash</code> for each first-party API. Copy the hash a provider needs (e.g.
      <code>edges.kedge.faros.sh</code> for kuery) into that provider's Helm values
      (<code>apiExport.edgesIdentityHash</code>).
    </p>
    <table class="w-full text-sm">
      <thead class="text-left text-[11px] uppercase text-text-muted">
        <tr>
          <th class="py-1 pr-4">Group / Resource</th>
          <th class="py-1 pr-4">Export</th>
          <th class="py-1">identityHash</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(id, i) in admin.identities" :key="i" class="border-t border-border-subtle/50">
          <td class="py-1.5 pr-4 text-text-primary">{{ id.resource }}.{{ id.group }}</td>
          <td class="py-1.5 pr-4 text-text-muted">{{ id.export }}</td>
          <td class="py-1.5 font-mono text-[11px] text-text-muted">{{ id.identityHash || '(not minted yet)' }}</td>
        </tr>
        <tr v-if="!admin.identities.length && !admin.loading">
          <td colspan="3" class="py-3 text-text-muted">No first-party identities found.</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
