<script setup lang="ts">
import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()
</script>

<template>
  <section>
    <h2 class="mb-1 text-base font-semibold text-text-primary">Organizations</h2>
    <p class="mb-4 text-sm text-text-muted">
      All organizations registered on the hub, with their workspaces and enabled providers.
    </p>

    <div v-if="!admin.orgs.length && !admin.loading" class="text-sm text-text-muted">
      No organizations found.
    </div>

    <div class="space-y-4">
      <div
        v-for="o in admin.orgs"
        :key="o.name"
        class="rounded-lg border border-border-subtle/60 p-4"
      >
        <!-- Org header -->
        <div class="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span class="text-sm font-semibold text-text-primary">{{ o.displayName || '—' }}</span>
          <span class="font-mono text-[11px] text-text-muted">{{ o.name }}</span>
          <span v-if="o.workspacePath" class="font-mono text-[11px] text-text-muted">
            {{ o.workspacePath }}
          </span>
        </div>

        <!-- Workspaces -->
        <table class="w-full text-sm">
          <thead class="text-left text-[11px] uppercase text-text-muted">
            <tr>
              <th class="py-1 pr-4">Workspace</th>
              <th class="py-1 pr-4">UUID</th>
              <th class="py-1 pr-4">Cluster</th>
              <th class="py-1">Providers</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="ws in o.workspaces"
              :key="ws.uuid"
              class="border-t border-border-subtle/40"
              :class="{ 'opacity-50': ws.deletionRequestedAt }"
            >
              <td class="py-1.5 pr-4 text-text-primary">
                {{ ws.displayName || '—' }}
                <span
                  v-if="ws.deletionRequestedAt"
                  class="ml-1 text-[10px] uppercase text-red-500"
                  >deleting</span
                >
              </td>
              <td class="py-1.5 pr-4 font-mono text-[11px] text-text-muted">{{ ws.uuid }}</td>
              <td class="py-1.5 pr-4 font-mono text-[11px] text-text-muted">
                {{ ws.clusterName || '—' }}
              </td>
              <td class="py-1.5">
                <div v-if="ws.providers.length" class="flex flex-wrap gap-1">
                  <span
                    v-for="p in ws.providers"
                    :key="p"
                    class="rounded border border-border-subtle bg-surface-overlay px-1.5 py-0.5 text-[11px] text-text-secondary"
                  >
                    {{ p }}
                  </span>
                </div>
                <span v-else class="text-[11px] text-text-muted">—</span>
              </td>
            </tr>
            <tr v-if="!o.workspaces.length">
              <td colspan="4" class="py-2 text-[11px] text-text-muted">No workspaces.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>
