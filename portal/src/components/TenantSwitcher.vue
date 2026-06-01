<!--
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<!--
Minimal Org / Workspace switcher rendered in the sidebar. Shows the
current selection as two stacked dropdowns; the portal user can
switch context across all Orgs they're a member of and the
Workspaces inside the active Org. Workspaces in their soft-delete
grace window are filtered out client-side (the REST layer also
suppresses them, this is belt-and-braces).

The component is intentionally simple. The detailed "create Org",
"create Workspace", "manage members" surfaces live in a separate
settings page that follows in a future iteration; this one ships
enough for users to navigate.
-->

<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useTenantStore } from '@/stores/tenant'

const tenant = useTenantStore()

onMounted(async () => {
  await tenant.fetchOrgs()
  if (tenant.orgUUID) {
    await tenant.fetchWorkspaces(tenant.orgUUID)
  }
})

// When the active org changes, load its workspaces if we haven't seen
// them yet. The store's selectOrg() does this too; this watch covers
// the case where the persisted orgUUID hydrates the store before
// fetchOrgs has run.
watch(
  () => tenant.orgUUID,
  (orgUUID) => {
    if (orgUUID && !tenant.workspacesByOrg[orgUUID]) {
      void tenant.fetchWorkspaces(orgUUID)
    }
  },
)

function onOrgChange(e: Event) {
  const value = (e.target as HTMLSelectElement).value
  if (value) tenant.selectOrg(value)
}

function onWorkspaceChange(e: Event) {
  const value = (e.target as HTMLSelectElement).value
  if (value) tenant.selectWorkspace(value)
}
</script>

<template>
  <div class="tenant-switcher flex flex-col gap-1 px-2 py-2 border-b border-border-default/40">
    <label class="text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">
      Organization
    </label>
    <select
      class="w-full rounded-md border border-border-default/40 bg-surface-overlay/40 px-2 py-1 text-[11px] text-text-primary focus:border-accent focus:outline-none"
      :value="tenant.orgUUID ?? ''"
      :disabled="tenant.loading || tenant.orgs.length === 0"
      @change="onOrgChange"
    >
      <option v-if="tenant.orgs.length === 0" value="">No orgs</option>
      <option v-for="o in tenant.orgs" :key="o.uuid" :value="o.uuid">
        {{ o.displayName }}{{ o.personal ? ' (personal)' : '' }}
      </option>
    </select>

    <label class="mt-2 text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">
      Workspace
    </label>
    <select
      class="w-full rounded-md border border-border-default/40 bg-surface-overlay/40 px-2 py-1 text-[11px] text-text-primary focus:border-accent focus:outline-none"
      :value="tenant.workspaceUUID ?? ''"
      :disabled="
        tenant.loading ||
        !tenant.orgUUID ||
        (tenant.workspacesByOrg[tenant.orgUUID] ?? []).length === 0
      "
      @change="onWorkspaceChange"
    >
      <option
        v-if="!tenant.orgUUID || (tenant.workspacesByOrg[tenant.orgUUID] ?? []).length === 0"
        value=""
      >
        No workspaces
      </option>
      <option
        v-for="w in tenant.workspacesByOrg[tenant.orgUUID ?? ''] ?? []"
        :key="w.uuid"
        :value="w.uuid"
      >
        {{ w.displayName || w.uuid }}
      </option>
    </select>

    <p
      v-if="tenant.error"
      class="mt-1 text-[10px] text-error"
    >
      {{ tenant.error }}
    </p>
  </div>
</template>
