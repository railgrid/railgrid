<script setup lang="ts">
// Shared organization/workspace roster. Creation lives in AddMemberDialog;
// the parent owns mutations and authoritative read state.

import { computed, nextTick, ref } from 'vue'
import { User as UserIcon } from 'lucide-vue-next'
import type { MemberRow } from '@/stores/tenant'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import type { TableFilterDefinition } from '@/portalkit/table'

const props = withDefaults(defineProps<{
  members: MemberRow[]
  loading: boolean
  // Omitted loaded preserves ResourceTable's loading-only contract for
  // callers that do not track the first authoritative read separately.
  loaded?: boolean | null
  error?: string | null
  stale?: boolean
  retryable?: boolean
  // Per-user in-flight flags owned by the page.
  busy: Record<string, boolean>
  // What an added member gains, e.g. "this organization" / "this workspace".
  // Rendered in the empty state so it never just says "No members." with no
  // hint about what adding one means.
  scopeLabel: string
  // Scope-specific accessible name for the shared roster table.
  tableLabel: string
  // readonly renders the roster without mutation affordances — roles as
  // static badges, no remove. Set for non-admin viewers,
  // whose writes would only 403 server-side; showing dead buttons and
  // letting the server reject them reads as a bug, not as permissions.
  readonly?: boolean
}>(), {
  loaded: null,
  error: null,
  stale: false,
  retryable: false,
})

const emit = defineEmits<{
  changeRole: [user: string, role: 'admin' | 'member']
  remove: [user: string]
  retry: []
}>()

const rosterRef = ref<HTMLElement | null>(null)
const query = ref('')
const tableRevision = ref(0)

// A deliberate success action may reveal a new member even when the saved
// search or role facet excludes them. Ordinary refreshes retain table state.
async function reveal(user: string) {
  query.value = user
  tableRevision.value++
  await nextTick()
  rosterRef.value?.scrollIntoView({ block: 'nearest' })
}

defineExpose({ reveal })

const memberColumns = computed(() => [
  { key: 'user', label: 'User', primary: true, fullValue: memberPrimaryValue },
  { key: 'role', label: 'Role' },
  ...(!props.readonly ? [{ key: 'actions', label: '', ariaLabel: 'Actions' }] : []),
])

const memberFilters: TableFilterDefinition[] = [{
  key: 'role',
  label: 'Role',
  allLabel: 'All roles',
  options: [
    { value: 'member', label: 'Member' },
    { value: 'admin', label: 'Admin' },
  ],
}]

// ResourceTable intentionally accepts record-shaped rows so it can remain a
// reusable table for every provider. Copy the typed store rows at this
// boundary; the parent still owns the canonical MemberRow values and all
// mutations continue to use the original user key.
const memberRows = computed<Record<string, unknown>[]>(() =>
  props.members.map((member) => ({ ...member })),
)

const memberEmptyText = computed(() => props.readonly
  ? 'No members yet.'
  : `No members yet. Anyone you add gains access to ${props.scopeLabel}.`)

function memberUser(row: Record<string, unknown>): string {
  return String(row.user ?? '')
}

function memberRole(row: Record<string, unknown>): 'admin' | 'member' {
  return row.role === 'admin' ? 'admin' : 'member'
}

function memberEmail(row: Record<string, unknown>): string {
  return String(row.email ?? '')
}

function memberDisplayName(row: Record<string, unknown>): string {
  return String(row.userDisplayName ?? '')
}

function memberPrimaryValue(row: Record<string, unknown>): string {
  const email = memberEmail(row)
  const displayName = memberDisplayName(row)
  return email && displayName ? `${email} · ${displayName}` : email || displayName || memberUser(row)
}

</script>

<template>
  <div ref="rosterRef">
    <ResourceTable
      :key="tableRevision"
      v-model:query="query"
      :columns="memberColumns"
      :rows="memberRows"
      :aria-label="tableLabel"
      row-key="user"
      :interactive="false"
      :loading="loading"
      :loaded="loaded"
      :error="error"
      :stale="stale"
      :retryable="retryable"
      :empty-text="memberEmptyText"
      searchable
      search-placeholder="Search members…"
      :search-keys="['user', 'email', 'userDisplayName', 'rbacIdentity']"
      :filters="memberFilters"
      paginated
      search-empty-text="No members match your search."
      filter-empty-text="No members match this role."
      combined-filter-empty-text="No members match your search and selected role."
      @retry="emit('retry')"
    >
      <!-- Lead with the person (email, falling back to display name), keep
           the CR name as a small mono sublabel — it is what API calls and
           RBAC are keyed on, so it stays visible/copyable. -->
      <template #user="{ row }">
        <div class="flex items-start gap-2">
          <UserIcon class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted/70" :stroke-width="1.75" />
          <div class="min-w-0">
            <template v-if="memberEmail(row) || memberDisplayName(row)">
              <div class="truncate text-[12px] text-text-primary">
                {{ memberEmail(row) || memberDisplayName(row) }}
                <span
                  v-if="memberDisplayName(row) && memberEmail(row)"
                  class="text-text-muted"
                > · {{ memberDisplayName(row) }}</span>
              </div>
              <div class="truncate font-mono text-[10px] text-text-muted">{{ memberUser(row) }}</div>
            </template>
            <span v-else class="font-mono text-[12px] text-text-secondary">{{ memberUser(row) }}</span>
          </div>
        </div>
      </template>
      <template #role="{ row }">
        <span
          v-if="readonly"
          class="k-badge k-badge--muted px-1.5 py-px text-[9px]"
        >{{ memberRole(row) }}</span>
        <select
          v-else
          class="k-input w-auto px-2 py-1 text-[12px] disabled:opacity-60"
          :aria-label="`Role for ${memberUser(row)} in ${scopeLabel}`"
          :value="memberRole(row)"
          :disabled="!!busy[memberUser(row)]"
          @change="(e) => emit('changeRole', memberUser(row), (e.target as HTMLSelectElement).value as 'admin' | 'member')"
        >
          <option value="member">member</option>
          <option value="admin">admin</option>
        </select>
      </template>
      <template #actions="{ row }">
        <div class="flex justify-end">
          <ResourceTableDeleteButton
            :label="`Remove ${memberUser(row)} from ${scopeLabel}`"
            :busy-label="`Removing ${memberUser(row)}…`"
            :busy="!!busy[memberUser(row)]"
            @click="emit('remove', memberUser(row))"
          />
        </div>
      </template>
    </ResourceTable>
  </div>
</template>
