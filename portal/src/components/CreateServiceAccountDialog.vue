<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { Loader2 } from 'lucide-vue-next'

const props = defineProps<{
  workspaceName: string
  organizationName: string
  create: (name: string, role: 'admin' | 'member') => Promise<boolean>
  errorMessage?: string | null
}>()
const emit = defineEmits<{ close: [] }>()
const id = useId()
const dialogRef = ref<HTMLDialogElement | null>(null)
const name = ref('')
const role = ref<'admin' | 'member'>('member')
const busy = ref(false)
const error = ref('')
const roleHelp = computed(() => role.value === 'admin'
  ? 'Admin grants administrative access to this workspace.'
  : 'Member grants member access to this workspace without administrative permissions.')
let disposed = false
let returnFocus: HTMLElement | null = null

function close() {
  if (busy.value || disposed) return
  dialogRef.value?.close()
  emit('close')
}

async function submit() {
  if (busy.value || disposed || !name.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    const succeeded = await props.create(name.value.trim(), role.value)
    await nextTick()
    if (disposed) return
    if (succeeded) {
      busy.value = false
      close()
    } else {
      error.value = props.errorMessage || 'Unable to create the service account. Try again.'
    }
  } catch (cause) {
    if (!disposed) error.value = cause instanceof Error ? cause.message : 'Unable to create the service account. Try again.'
  } finally {
    if (!disposed) busy.value = false
  }
}

onMounted(() => {
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  dialogRef.value?.showModal()
})
onBeforeUnmount(() => {
  disposed = true
  dialogRef.value?.close()
  if (returnFocus?.isConnected) returnFocus.focus()
})
</script>

<template>
  <Teleport to="body">
    <dialog
      ref="dialogRef"
      class="create-service-account-dialog k-modal"
      :aria-labelledby="`${id}-title`"
      :aria-describedby="`${id}-description`"
      :aria-busy="busy"
      @cancel.prevent="close"
    >
      <form @submit.prevent="submit">
        <h2 :id="`${id}-title`" class="k-modal__title">Create service account</h2>
        <p :id="`${id}-description`" class="break-words text-sm text-text-secondary">
          Create a machine identity in workspace <strong>{{ workspaceName }}</strong>, organization <strong>{{ organizationName }}</strong>.
        </p>
        <div class="mt-5 space-y-4">
          <div>
            <label :for="`${id}-name`" class="mb-2 block text-sm font-medium text-text-primary">Service account name</label>
            <input
              :id="`${id}-name`"
              v-model="name"
              class="k-input"
              type="text"
              placeholder="e.g. Deployment automation"
              autocomplete="off"
              autofocus
              required
              :readonly="busy"
              :aria-describedby="error ? `${id}-error` : undefined"
            />
          </div>
          <div>
            <label :for="`${id}-role`" class="mb-2 block text-sm font-medium text-text-primary">Role</label>
            <select :id="`${id}-role`" v-model="role" class="k-input" :disabled="busy" :aria-describedby="`${id}-role-help`">
              <option value="member">Member</option>
              <option value="admin">Admin</option>
            </select>
            <p :id="`${id}-role-help`" class="mt-2 text-sm text-text-secondary">{{ roleHelp }}</p>
          </div>
          <p class="text-sm text-text-secondary">After creating the account, use Issue token in the table to generate a credential.</p>
        </div>
        <p v-if="error" :id="`${id}-error`" role="alert" class="mt-4 text-sm text-danger">{{ error }}</p>
        <div class="k-modal__actions">
          <button type="button" class="k-btn k-btn--secondary" :disabled="busy" @click="close">Cancel</button>
          <button type="submit" class="k-btn k-btn--primary" :disabled="busy || !name.trim()">
            <Loader2 v-if="busy" class="h-4 w-4 animate-spin" aria-hidden="true" />
            {{ busy ? 'Creating…' : 'Create service account' }}
          </button>
        </div>
      </form>
    </dialog>
  </Teleport>
</template>

<style scoped>
.create-service-account-dialog {
  margin: auto;
  max-width: calc(100vw - 32px);
  max-height: calc(100dvh - 32px);
  overflow-y: auto;
}
.create-service-account-dialog::backdrop {
  background: color-mix(in srgb, var(--color-surface) 60%, transparent);
}
</style>
