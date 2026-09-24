<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2 } from 'lucide-vue-next'
import { authFetch } from '@/auth/session'
import { useAuthStore } from '@/stores/auth'
import { isWorkspaceUsable, useTenantStore, type WorkspaceRow } from '@/stores/tenant'

const emit = defineEmits<{ close: [] }>()
const tenant = useTenantStore()
const auth = useAuthStore()
const openingToken = auth.token
const router = useRouter()
const openingPath = router.currentRoute.value.fullPath
const orgID = tenant.orgUUID
const orgName = tenant.activeOrg?.displayName || 'this organization'
const dialogRef = ref<HTMLDialogElement | null>(null)
const name = ref('')
const busy = ref(false)
const created = ref<WorkspaceRow | null>(null)
const error = ref('')
const timedOut = ref(false)
const id = useId()
const canCreate = computed(() => tenant.orgUUID === orgID && !!tenant.activeOrg &&
  tenant.orgLoadState === 'ready' && !tenant.orgError && !tenant.activeOrg.deletionRequestedAt &&
  (tenant.activeOrg.role === 'admin' || tenant.activeOrg.workspaceCreation === 'members'))
let generation = 0
let disposed = false
let timer: ReturnType<typeof setTimeout> | undefined
let started = 0

function current(revision: number) {
  return !disposed && revision === generation && tenant.orgUUID === orgID && auth.token === openingToken
}

function close() {
  if (disposed) return
  disposed = true
  generation++
  clearTimeout(timer)
  dialogRef.value?.close()
  emit('close')
}

async function checkReady(revision = generation) {
  if (!orgID || !created.value || !current(revision)) return
  busy.value = true
  error.value = ''
  try {
    // Read only the new workspace: polling the shared list would repeatedly
    // suspend the current workspace while its hydration state is loading.
    const response = await authFetch(`/api/orgs/${encodeURIComponent(orgID)}/workspaces/${encodeURIComponent(created.value.uuid)}`, {
      // Workspace reads require headers matching the URL. The operating
      // workspace remains selected until this new workspace can be entered.
      headers: { 'X-Railgrid-Org': orgID, 'X-Railgrid-Workspace': created.value.uuid },
    })
    if (!current(revision)) return
    if (!response.ok) {
      const status = await response.json().catch(() => null) as { message?: string } | null
      throw new Error(`Your workspace was created, but its readiness could not be checked (HTTP ${response.status}). ${status?.message || 'Try again.'}`)
    }
    const workspace = await response.json() as WorkspaceRow
    if (!current(revision)) return
    if (workspace.uuid !== created.value.uuid || workspace.orgUUID !== orgID) {
      throw new Error('The workspace response could not be verified. Try again.')
    }
    if (workspace.deletionRequestedAt) throw new Error('This workspace is being deleted and cannot be opened.')
    if (workspace && isWorkspaceUsable(workspace)) {
      const transition = tenant.beginWorkspaceTransition()
      try {
        const failure = await router.push({ name: 'dashboard', params: { orgID, workspaceID: workspace.uuid } })
        if (!current(revision)) return
        if (failure || tenant.workspaceUUID !== workspace.uuid) {
          throw new Error('Your workspace is ready, but could not be opened. Try again.')
        }
        close()
      } finally {
        tenant.endWorkspaceTransition(transition)
      }
      return
    }
    timedOut.value = Date.now() - started >= 60_000
    if (timedOut.value) busy.value = false
    else timer = setTimeout(() => { void checkReady(revision) }, 2500)
  } catch (e) {
    if (!current(revision)) return
    error.value = e instanceof Error ? e.message : 'Unable to open your workspace. Try again.'
    busy.value = false
  }
}

async function submit() {
  if (busy.value || !orgID || disposed) return
  if (created.value) {
    started = Date.now()
    timedOut.value = false
    await checkReady()
    return
  }
  if (!name.value.trim() || !canCreate.value) return
  const revision = generation
  busy.value = true
  error.value = ''
  try {
    // This dialog owns creation errors and loading state. Refreshing the
    // shared list here would suspend the workspace the user is still in.
    const response = await authFetch(`/api/orgs/${encodeURIComponent(orgID)}/workspaces`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Railgrid-Org': orgID },
      body: JSON.stringify({ displayName: name.value.trim() }),
    })
    if (!response.ok) {
      const status = await response.json().catch(() => null) as { message?: string } | null
      throw new Error(status?.message || `Unable to create the workspace (HTTP ${response.status}). Try again.`)
    }
    const result = await response.json() as WorkspaceRow
    if (!result.uuid || result.orgUUID !== orgID) throw new Error('The workspace response could not be verified. Try again.')
    // Keep a successful creation discoverable even if the dialog was closed
    // while POST was in flight, without changing selection or hydration.
    if (tenant.orgUUID === orgID && auth.token === openingToken) {
      const rows = tenant.workspacesByOrg[orgID] ?? []
      tenant.workspacesByOrg[orgID] = [...rows.filter(row => row.uuid !== result.uuid), result]
    }
    if (!current(revision)) return
    created.value = result
    started = Date.now()
    await checkReady(revision)
  } catch (e) {
    if (!current(revision)) return
    error.value = e instanceof Error ? e.message : 'Unable to create the workspace. Try again.'
    busy.value = false
  }
}

watch([() => tenant.orgUUID, () => router.currentRoute.value.fullPath, () => auth.token], () => {
  if (tenant.orgUUID !== orgID || router.currentRoute.value.fullPath !== openingPath || auth.token !== openingToken) close()
})
onMounted(() => dialogRef.value?.showModal())
onBeforeUnmount(() => {
  disposed = true
  generation++
  clearTimeout(timer)
  dialogRef.value?.close()
})
</script>

<template>
  <Teleport to="body">
    <dialog
      ref="dialogRef"
      class="create-workspace-dialog k-modal"
      :aria-labelledby="`${id}-title`"
      :aria-describedby="`${id}-description`"
      @cancel.prevent="close"
    >
      <form @submit.prevent="submit">
        <h2 :id="`${id}-title`" class="k-modal__title">Create workspace</h2>
        <p :id="`${id}-description`" class="break-words text-sm text-text-secondary">
          Create a workspace in {{ orgName }}. You’ll enter it when it’s ready.
        </p>
        <label :for="`${id}-name`" class="mb-2 mt-5 block text-sm font-medium text-text-primary">Workspace name</label>
        <input
          :id="`${id}-name`"
          v-model="name"
          type="text"
          class="k-input"
          placeholder="e.g. Development"
          autocomplete="off"
          autofocus
          required
          :readonly="busy || !!created"
          :aria-describedby="error ? `${id}-error` : undefined"
        />
        <p v-if="error" :id="`${id}-error`" role="alert" class="mt-4 text-sm text-danger">{{ error }}</p>
        <div v-if="busy || created" role="status" aria-live="polite" class="mt-4 text-sm text-text-secondary">
          <p v-if="!error" class="flex items-center gap-2">
            <Loader2 v-if="busy" class="h-4 w-4 shrink-0 animate-spin" aria-hidden="true" />
            {{ created ? (timedOut ? 'Your workspace is taking longer to prepare. Check again in a moment.' : 'Your workspace has been created. Preparing to open it…') : 'Creating your workspace…' }}
          </p>
          <p class="mt-2">You can close this window and open the workspace from the picker later.</p>
        </div>
        <div class="k-modal__actions">
          <button type="button" class="k-btn k-btn--secondary" @click="close">{{ busy || created ? 'Close' : 'Cancel' }}</button>
          <button type="submit" class="k-btn k-btn--primary" :disabled="busy || (!created && (!name.trim() || !canCreate))">
            {{ busy ? (created ? 'Preparing…' : 'Creating…') : created ? 'Check again' : 'Create workspace' }}
          </button>
        </div>
      </form>
    </dialog>
  </Teleport>
</template>

<style scoped>
.create-workspace-dialog {
  margin: auto;
  max-width: calc(100vw - 32px);
  max-height: calc(100dvh - 32px);
  overflow-y: auto;
}
.create-workspace-dialog::backdrop {
  background: color-mix(in srgb, var(--color-surface) 60%, transparent);
}
</style>
