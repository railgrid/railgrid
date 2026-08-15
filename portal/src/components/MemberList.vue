<script setup lang="ts">
// One member roster: add form + table with per-row role select and remove.
// The tenant settings page renders this twice — once org-scoped, once
// workspace-scoped — with identical mechanics and different handlers; the
// component keeps the two visually and behaviourally in lockstep instead of
// letting two hand-maintained copies drift.
//
// All mutations are emitted upward: the parent owns the store calls, the
// busy bookkeeping, and the reload. This stays a dumb roster.

import { ref } from 'vue'
import { Loader2, Plus, Trash2, User as UserIcon } from 'lucide-vue-next'
import type { MemberRow } from '@/stores/tenant'

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
        class="min-w-[200px] flex-1 rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
        placeholder="email or user UUID"
        @keyup.enter="submit"
      />
      <select
        v-model="newRole"
        class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
        title="Admins manage members and settings; members use what's already here."
      >
        <option value="member">member</option>
        <option value="admin">admin</option>
      </select>
      <button
        class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-60"
        :disabled="!!busy.__new__ || !newUser.trim()"
        @click="submit"
      >
        <Loader2 v-if="busy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
        <Plus v-else class="h-3 w-3" :stroke-width="2" />
        Add
      </button>
    </div>

    <div v-if="loading" class="mt-3 text-sm text-text-muted">Loading members…</div>
    <div v-else-if="members.length === 0" class="mt-3 text-sm text-text-muted">
      No members yet.<template v-if="!readonly"> Anyone you add gains access to {{ scopeLabel }}.</template>
    </div>
    <table v-else class="mt-3 w-full text-sm">
      <thead>
        <tr class="text-left text-[10px] font-semibold uppercase tracking-wider text-text-muted">
          <th class="py-2 pr-3">User</th>
          <th class="py-2 pr-3">Role</th>
          <th v-if="!readonly" class="py-2 pr-0 text-right">Actions</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border-default/30">
        <tr v-for="m in members" :key="m.user">
          <!-- Lead with the person (email, falling back to display name),
               keep the CR name as a small mono sublabel — it's what API
               calls and RBAC are keyed on, so it stays visible/copyable,
               but "static-user-47b9dce0…" must not be the headline. -->
          <td class="py-2 pr-3">
            <div class="flex items-start gap-2">
              <UserIcon class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted/70" :stroke-width="1.75" />
              <div class="min-w-0">
                <template v-if="m.email || m.userDisplayName">
                  <div class="truncate text-[12px] text-text-primary">
                    {{ m.email || m.userDisplayName }}
                    <span
                      v-if="m.userDisplayName && m.email"
                      class="text-text-muted"
                    > · {{ m.userDisplayName }}</span>
                  </div>
                  <div class="truncate font-mono text-[10px] text-text-muted">{{ m.user }}</div>
                </template>
                <span v-else class="font-mono text-[12px] text-text-secondary">{{ m.user }}</span>
              </div>
            </div>
          </td>
          <td class="py-2 pr-3">
            <span
              v-if="readonly"
              class="rounded-sm border border-border-default/50 bg-surface-overlay px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-text-muted"
            >{{ m.role }}</span>
            <select
              v-else
              class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-[12px] text-text-primary focus:border-accent focus:outline-none disabled:opacity-60"
              :value="m.role"
              :disabled="!!busy[m.user]"
              @change="(e) => emit('changeRole', m.user, (e.target as HTMLSelectElement).value as 'admin' | 'member')"
            >
              <option value="member">member</option>
              <option value="admin">admin</option>
            </select>
          </td>
          <td v-if="!readonly" class="py-2 pr-0 text-right">
            <button
              class="rounded-md border border-danger/30 bg-danger-subtle px-2 py-1 text-[11px] font-medium text-danger hover:bg-danger/15 disabled:opacity-50"
              :disabled="!!busy[m.user]"
              @click="emit('remove', m.user)"
            >
              <Loader2 v-if="busy[m.user]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
              <Trash2 v-else class="inline h-3 w-3" :stroke-width="2" />
              Remove
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
