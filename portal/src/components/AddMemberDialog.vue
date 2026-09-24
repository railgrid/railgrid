<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { Loader2, User as UserIcon } from 'lucide-vue-next'
import type { MemberRow } from '@/stores/tenant'
import { useUserSuggestions, type UserSuggestion } from '@/composables/useUserSuggestions'

const props = defineProps<{
  members: MemberRow[]
  scope: 'organization' | 'workspace'
  scopeName: string
  organizationName: string
  organizationSettingsPath?: string
  canManageOrganization?: boolean
  add: (user: string, role: 'admin' | 'member') => Promise<boolean>
  errorMessage?: string | null
}>()
const emit = defineEmits<{ close: [] }>()
const dialogRef = ref<HTMLDialogElement | null>(null)
const newUser = ref('')
const newRole = ref<'admin' | 'member'>('member')
const busy = ref(false)
const error = ref('')
const roleHelp = computed(() => {
  if (props.scope === 'organization') {
    return newRole.value === 'admin'
      ? 'Admins manage organization members and settings, and have administrative access to all its workspaces.'
      : 'Members can access the organization. Access to each workspace must be granted separately.'
  }
  return newRole.value === 'admin'
    ? 'Admins have full workspace access and can manage its members and settings.'
    : 'Members can edit resources in this workspace.'
})
const id = useId()
let disposed = false
let trigger: HTMLElement | null = null

const { suggestions: foundUsers } = useUserSuggestions(newUser)
const suggestions = computed(() => {
  const existing = new Set(props.members.map((member) => member.user))
  return foundUsers.value.filter((person) => !existing.has(person.user))
})
const suggestionsOpen = ref(false)
const activeSuggestion = ref(-1)
const showSuggestions = computed(() => suggestionsOpen.value && suggestions.value.length > 0)
const listboxId = `${id}-suggestions`

function suggestionId(index: number): string {
  return `${listboxId}-option-${index}`
}

function pickSuggestion(person: UserSuggestion) {
  newUser.value = person.memberId
  suggestionsOpen.value = false
  activeSuggestion.value = -1
}

function onUserKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && showSuggestions.value) {
    event.preventDefault()
    event.stopPropagation()
    suggestionsOpen.value = false
    activeSuggestion.value = -1
    return
  }
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    if (!suggestions.value.length) return
    event.preventDefault()
    suggestionsOpen.value = true
    const count = suggestions.value.length
    activeSuggestion.value = activeSuggestion.value < 0
      ? (event.key === 'ArrowDown' ? 0 : count - 1)
      : (activeSuggestion.value + (event.key === 'ArrowDown' ? 1 : -1) + count) % count
  } else if (event.key === 'Enter' && showSuggestions.value && activeSuggestion.value >= 0) {
    event.preventDefault()
    const person = suggestions.value[activeSuggestion.value]
    if (person) pickSuggestion(person)
  }
}

function close() {
  if (busy.value || disposed) return
  dialogRef.value?.close()
  if (trigger?.isConnected) trigger.focus()
  emit('close')
}

async function submit() {
  const user = newUser.value.trim()
  if (!user || busy.value || disposed) return
  suggestionsOpen.value = false
  busy.value = true
  error.value = ''
  try {
    const ok = await props.add(user, newRole.value)
    await nextTick()
    if (disposed) return
    busy.value = false
    if (ok) close()
    else error.value = props.errorMessage || 'Unable to add this member. Check their email or member ID and try again.'
  } catch (cause) {
    if (disposed) return
    error.value = cause instanceof Error ? cause.message : 'Unable to add this member. Try again.'
  } finally {
    if (!disposed) busy.value = false
  }
}

onMounted(() => {
  trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  dialogRef.value?.showModal()
})
onBeforeUnmount(() => {
  disposed = true
  dialogRef.value?.close()
  if (trigger?.isConnected) trigger.focus()
})
</script>

<template>
  <Teleport to="body">
    <dialog
      ref="dialogRef"
      class="add-member-dialog k-modal"
      :aria-labelledby="`${id}-title`"
      :aria-describedby="`${id}-description`"
      :aria-busy="busy"
      @cancel.prevent="close"
    >
      <form @submit.prevent="submit">
        <h2 :id="`${id}-title`" class="k-modal__title">Add {{ scope }} member</h2>
        <p :id="`${id}-description`" class="break-words text-sm text-text-secondary">
          <template v-if="scope === 'workspace'">Workspace: {{ scopeName }} · Organization: {{ organizationName }}</template>
          <template v-else>Organization: {{ organizationName }}</template>
        </p>

        <div class="mt-5">
          <label :for="`${id}-user`" class="mb-2 block text-sm font-medium text-text-primary">Email or member ID</label>
          <div class="relative">
            <input
              :id="`${id}-user`"
              v-model="newUser"
              class="k-input"
              placeholder="e.g. person@example.com"
              role="combobox"
              aria-autocomplete="list"
              :aria-expanded="showSuggestions"
              :aria-controls="listboxId"
              :aria-activedescendant="showSuggestions && activeSuggestion >= 0 ? suggestionId(activeSuggestion) : undefined"
              :aria-describedby="`${id}-user-help`"
              autocomplete="off"
              autofocus
              required
              :disabled="busy"
              @input="suggestionsOpen = true; activeSuggestion = -1"
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
                v-for="(person, index) in suggestions"
                :id="suggestionId(index)"
                :key="person.user"
                role="option"
                :aria-selected="index === activeSuggestion"
                class="k-menu-item cursor-pointer"
                :class="{ 'is-selected': index === activeSuggestion }"
                @mousedown.prevent="pickSuggestion(person)"
              >
                <UserIcon class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
                <span class="min-w-0 truncate text-sm text-text-primary">{{ person.memberId }}</span>
                <span v-if="person.displayName && person.displayName !== person.memberId" class="min-w-0 truncate text-xs text-text-muted">{{ person.displayName }}</span>
              </div>
            </div>
          </div>
          <p :id="`${id}-user-help`" class="mt-2 text-sm text-text-secondary">Use an existing user's email or the member ID from their account menu.</p>
        </div>

        <div class="mt-4">
          <label :for="`${id}-role`" class="mb-2 block text-sm font-medium text-text-primary">Role</label>
          <select :id="`${id}-role`" v-model="newRole" class="k-input" :disabled="busy" :aria-describedby="`${id}-role-help`">
            <option value="member">Member</option>
            <option value="admin">Admin</option>
          </select>
          <p :id="`${id}-role-help`" class="mt-2 text-sm text-text-secondary">{{ roleHelp }}</p>
        </div>

        <div v-if="scope === 'workspace'" class="mt-4 text-sm text-text-secondary">
          <p>
            Adding someone here grants access to this workspace only. To add them to the organization, add them in
            <RouterLink v-if="organizationSettingsPath && !busy" :to="organizationSettingsPath" class="text-accent underline underline-offset-2" @click="close">Organization settings</RouterLink>
            <span v-else>Organization settings</span> first.
          </p>
          <p v-if="!canManageOrganization" class="mt-2">Only organization admins can add organization members.</p>
        </div>

        <p v-if="error" role="alert" class="mt-4 text-sm text-danger">{{ error }}</p>
        <div class="k-modal__actions">
          <button type="button" class="k-btn k-btn--secondary" :disabled="busy" @click="close">Cancel</button>
          <button type="submit" class="k-btn k-btn--primary" :disabled="busy || !newUser.trim()">
            <Loader2 v-if="busy" class="h-4 w-4 animate-spin" aria-hidden="true" />
            {{ busy ? 'Adding…' : 'Add member' }}
          </button>
        </div>
      </form>
    </dialog>
  </Teleport>
</template>

<style scoped>
.add-member-dialog {
  margin: auto;
  max-width: calc(100vw - 32px);
  max-height: calc(100dvh - 32px);
  overflow-y: auto;
}
.add-member-dialog::backdrop {
  background: color-mix(in srgb, var(--color-surface) 60%, transparent);
}
</style>
