<script setup lang="ts">
// One member roster: add form + table with per-row role select and remove.
// The tenant settings page renders this twice — once org-scoped, once
// workspace-scoped — with identical mechanics and different handlers; the
// component keeps the two visually and behaviourally in lockstep instead of
// letting two hand-maintained copies drift.
//
// All mutations are emitted upward: the parent owns the store calls, the
// busy bookkeeping, and the reload. This stays a dumb roster.

import { computed, ref } from 'vue'
import { Loader2, Plus, User as UserIcon } from 'lucide-vue-next'
import type { MemberRow } from '@/stores/tenant'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'

const props = defineProps<{
  members: MemberRow[]
  loading: boolean
  // Per-user in-flight flags plus '__new__' for the add form; same shape the
  // page already tracks for its store calls.
  busy: Record<string, boolean>
  // What an added member gains, e.g. "this organization" / "this workspace".
  // Rendered in the empty state so it never just says "No members." with no
  // hint about what adding one means.
  scopeLabel: string
  // add is a function prop (not an emit) so the component can await the
  // outcome: the typed identifier is only cleared when the add succeeded,
  // instead of being thrown away under a failure toast.
  add: (user: string, role: 'admin' | 'member') => Promise<boolean>
  // readonly renders the roster without any mutation affordances — no add
  // form, roles as static badges, no remove. Set for non-admin viewers,
  // whose writes would only 403 server-side; showing dead buttons and
  // letting the server reject them reads as a bug, not as permissions.
  readonly?: boolean
}>()

const emit = defineEmits<{
  changeRole: [user: string, role: 'admin' | 'member']
  remove: [user: string]
}>()

const newUser = ref('')
const newRole = ref<'admin' | 'member'>('member')

const memberColumns = computed(() => [
  { key: 'user', label: 'User' },
  { key: 'role', label: 'Role' },
  ...(!props.readonly ? [{ key: 'actions', label: '' }] : []),
])

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

async function submit() {
  const u = newUser.value.trim()
  if (!u || props.busy.__new__) return
  const ok = await props.add(u, newRole.value)
  if (ok) {
    newUser.value = ''
    newRole.value = 'member'
  }
}
</script>

<template>
  <div>
    <div v-if="!readonly" class="flex flex-wrap items-center gap-2">
      <input
        v-model="newUser"
        class="k-input min-w-[200px] w-auto flex-1 text-sm"
        placeholder="email or user UUID"
        aria-label="Member email or user UUID"
        @keyup.enter="submit"
      />
      <select
        v-model="newRole"
        class="k-input w-auto text-sm"
        aria-label="Role for new member"
        title="Admins manage members and settings; members use what's already here."
      >
        <option value="member">member</option>
        <option value="admin">admin</option>
      </select>
      <button
        type="button"
        class="k-btn k-btn--primary px-3 py-1.5 text-[12px]"
        :disabled="!!busy.__new__ || !newUser.trim()"
        @click="submit"
      >
        <Loader2 v-if="busy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
        <Plus v-else class="h-3 w-3" :stroke-width="2" />
        Add
      </button>
    </div>

    <ResourceTable
      class="mt-3"
      :columns="memberColumns"
      :rows="memberRows"
      variant="simple"
      row-key="user"
      :interactive="false"
      :loading="loading"
      :empty-text="memberEmptyText"
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
