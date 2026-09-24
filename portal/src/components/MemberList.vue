<script setup lang="ts">
// One member roster: add form + table with per-row role select and remove.
// The tenant settings page renders this twice — once org-scoped, once
// workspace-scoped — with identical mechanics and different handlers; the
// component keeps the two visually and behaviourally in lockstep instead of
// letting two hand-maintained copies drift.
//
// All mutations are emitted upward: the parent owns the store calls, the
// busy bookkeeping, and the reload. This stays a dumb roster.

import { computed, ref, useId } from 'vue'
import { Loader2, Plus, User as UserIcon } from 'lucide-vue-next'
import type { MemberRow } from '@/stores/tenant'
import { useUserSuggestions, type UserSuggestion } from '@/composables/useUserSuggestions'
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
  // Per-user in-flight flags plus '__new__' for the add form; same shape the
  // page already tracks for its store calls.
  busy: Record<string, boolean>
  // What an added member gains, e.g. "this organization" / "this workspace".
  // Rendered in the empty state so it never just says "No members." with no
  // hint about what adding one means.
  scopeLabel: string
  // Scope-specific accessible name for the shared roster table.
  tableLabel: string
  // add is a function prop (not an emit) so the component can await the
  // outcome: the typed identifier is only cleared when the add succeeded,
  // instead of being thrown away under a failure toast.
  add: (user: string, role: 'admin' | 'member') => Promise<boolean>
  // readonly renders the roster without any mutation affordances — no add
  // form, roles as static badges, no remove. Set for non-admin viewers,
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

const newUser = ref('')
const newRole = ref<'admin' | 'member'>('member')

// Suggestions for the add box, from the rate-limited user search. People who
// are already members are not suggested again.
const { suggestions: foundUsers } = useUserSuggestions(newUser)
const suggestions = computed(() => {
  const existing = new Set(props.members.map((m) => m.user))
  return foundUsers.value.filter((s) => !existing.has(s.user))
})
const suggestionsOpen = ref(false)
const activeSuggestion = ref(-1)
const listboxId = useId()
const showSuggestions = computed(() => suggestionsOpen.value && suggestions.value.length > 0)

function suggestionId(index: number): string {
  return `${listboxId}-option-${index}`
}

function onUserInput() {
  suggestionsOpen.value = true
  activeSuggestion.value = -1
}

function pickSuggestion(s: UserSuggestion) {
  newUser.value = s.memberId
  suggestionsOpen.value = false
  activeSuggestion.value = -1
}

function onUserKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && showSuggestions.value) {
    event.preventDefault()
    suggestionsOpen.value = false
    return
  }
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    if (!suggestions.value.length) return
    event.preventDefault()
    suggestionsOpen.value = true
    const n = suggestions.value.length
    const step = event.key === 'ArrowDown' ? 1 : -1
    activeSuggestion.value = (activeSuggestion.value + step + n) % n
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    const picked = showSuggestions.value ? suggestions.value[activeSuggestion.value] : undefined
    if (picked) pickSuggestion(picked)
    else void submit()
  }
}

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

async function submit() {
  const u = newUser.value.trim()
  if (!u || props.busy.__new__) return
  suggestionsOpen.value = false
  const ok = await props.add(u, newRole.value)
  if (ok) {
    newUser.value = ''
    newRole.value = 'member'
  }
}
</script>

<template>
  <div>
    <div v-if="!readonly && (loaded !== false || !error)" class="mb-4 flex flex-wrap items-center gap-2">
      <div class="relative min-w-[200px] flex-1">
        <input
          v-model="newUser"
          class="k-input w-full text-sm"
          placeholder="email or member ID"
          aria-label="Member email or member ID"
          title="Start typing an email, or a member ID such as railgrid:static:02d4b…, to see matching people. Everyone can copy their member ID from their account menu."
          role="combobox"
          aria-autocomplete="list"
          :aria-expanded="showSuggestions"
          :aria-controls="listboxId"
          :aria-activedescendant="showSuggestions && activeSuggestion >= 0 ? suggestionId(activeSuggestion) : undefined"
          autocomplete="off"
          @input="onUserInput"
          @keydown="onUserKeydown"
          @blur="suggestionsOpen = false"
        />
        <div
          v-show="showSuggestions"
          :id="listboxId"
          role="listbox"
          aria-label="Matching people"
          class="k-menu absolute left-0 right-0 top-full z-20 mt-1"
        >
          <div
            v-for="(s, i) in suggestions"
            :id="suggestionId(i)"
            :key="s.user"
            role="option"
            :aria-selected="i === activeSuggestion"
            class="k-menu-item cursor-pointer"
            :class="i === activeSuggestion ? 'is-selected' : ''"
            @mousedown.prevent="pickSuggestion(s)"
          >
            <UserIcon class="h-3.5 w-3.5 shrink-0 text-text-muted/70" :stroke-width="1.75" aria-hidden="true" />
            <span class="min-w-0 truncate text-[12px] text-text-primary" :class="s.email ? '' : 'font-mono'">{{ s.memberId }}</span>
            <span
              v-if="s.displayName && s.displayName !== s.memberId"
              class="min-w-0 truncate text-[11px] text-text-muted"
            >{{ s.displayName }}</span>
          </div>
        </div>
      </div>
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
      :search-keys="['user', 'email', 'userDisplayName']"
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
