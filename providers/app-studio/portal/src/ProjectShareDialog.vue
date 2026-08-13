<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, Copy, Link2, Loader2, Users, X } from 'lucide-vue-next'
import { copyTextWithFallback } from './clipboard'
import { confirmState } from './portalkit/confirm'
import type { ProjectPublishingGrant, ProjectPublishingMember, ProjectPublishingMode } from './types'

type ShareLoadState = 'idle' | 'loading' | 'partial' | 'ready' | 'error'

const props = withDefaults(defineProps<{
  open: boolean
  projectName: string
  mode: ProjectPublishingMode
  published: boolean
  publicationStateAvailable: boolean
  publication?: {
    mode?: ProjectPublishingMode | null
    url?: string | null
    ready?: boolean
    error?: string | null
  } | null
  productionURL?: string
  productionReady: boolean
  members: ProjectPublishingMember[]
  grants: ProjectPublishingGrant[]
  busy?: boolean
  busyAction?: null | 'save' | 'grant' | 'invite' | 'revoke' | 'disable'
  busyTarget?: string
  loading?: boolean
  error?: string | null
  loadState?: ShareLoadState
  loadError?: string | null
  membersError?: string | null
}>(), {
  publication: null,
  publicationStateAvailable: false,
  productionURL: '',
  busy: false,
  busyAction: null,
  busyTarget: '',
  loading: false,
  error: null,
  loadState: 'ready',
  loadError: null,
  membersError: null,
})

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'update:mode', mode: ProjectPublishingMode): void
  (event: 'save'): void
  (event: 'grant', user: string): void
  (event: 'invite', email: string): void
  (event: 'revoke', grant: string): void
  (event: 'disable'): void
  (event: 'open-production-settings'): void
  (event: 'retry'): void
}>()

const selectedMember = ref('')
const inviteEmail = ref('')
const copyState = ref<'idle' | 'copied' | 'error'>('idle')
const initialMode = ref(props.mode)
const modeTouched = ref(false)
const dialogCloseButton = ref<HTMLButtonElement | null>(null)
const linkInput = ref<HTMLInputElement | null>(null)
const dialogRef = ref<HTMLElement | null>(null)

const selectedMode = computed({
  get: () => props.mode,
  set: (mode: ProjectPublishingMode) => {
    if (!props.publicationStateAvailable) return
    modeTouched.value = true
    emit('update:mode', mode)
  },
})
const link = computed(() => props.productionURL.trim() || props.publication?.url?.trim() || '')
const activeGrants = computed(() => props.grants.filter((grant) => !grant.revoked))
const availableMembers = computed(() => props.members.filter((member) => (
  !activeGrants.value.some((grant) => grant.user === member.user)
)))
const showViewers = computed(() => props.publicationStateAvailable && props.published && selectedMode.value === 'restricted')
const modeDirty = computed(() => props.published && selectedMode.value !== initialMode.value)
const savedRestricted = computed(() => props.publicationStateAvailable && props.published && props.publication?.mode === 'restricted')
// `busy` remains the broad mutation gate so controls cannot issue concurrent
// writes. `busyAction` keeps the visible pending state on the control that
// initiated the write instead of making every mutation look equally active.
const saveBusy = computed(() => props.busy && (props.busyAction === null || props.busyAction === 'save'))
const grantBusy = computed(() => props.busy && props.busyAction === 'grant')
const inviteBusy = computed(() => props.busy && props.busyAction === 'invite')
const disableBusy = computed(() => props.busy && props.busyAction === 'disable')
function revokeBusy(grant: string) {
  return props.busy && props.busyAction === 'revoke' && props.busyTarget === grant
}
const primaryLabel = computed(() => {
  if (saveBusy.value) return props.published ? 'Saving access…' : 'Publishing…'
  if (!props.published) return 'Publish app'
  return modeDirty.value ? 'Save access' : 'Done'
})
// An access change on a promoted app is an intent write the platform accepts
// at any time — the publication state machine reports Pending honestly while
// the gate converges, so a rolling or briefly-unready deployment must not
// block flipping public/invite-only. Only the initial publish of a
// never-promoted project still waits for a ready production deployment.
const canSave = computed(() => (
  props.publicationStateAvailable &&
  !props.loading && props.loadState !== 'error' && !props.busy && (props.published || props.productionReady)
))
// Grants are mutations against the saved restricted publication. A draft mode
// must be saved first so a public publication cannot receive a viewer grant.
const canAddMember = computed(() => (
  props.publicationStateAvailable && savedRestricted.value && !modeDirty.value && !!selectedMember.value && !props.busy && !props.loading
))
// Invite-by-email shares with someone not on the platform yet: the hub
// pre-provisions their account + org membership and the grant applies at
// their first sign-in. Same saved-restricted gating as member grants.
const inviteEmailValid = computed(() => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(inviteEmail.value.trim()))
const canInvite = computed(() => (
  props.publicationStateAvailable && savedRestricted.value && !modeDirty.value && inviteEmailValid.value && !props.busy && !props.loading
))

function inviteByEmail() {
  const email = inviteEmail.value.trim()
  if (!canInvite.value || !email) return
  emit('invite', email)
  inviteEmail.value = ''
}

async function focusDialog() {
  await nextTick()
  dialogCloseButton.value?.focus()
}

watch(() => props.open, (open) => {
  if (open) {
    initialMode.value = props.mode
    modeTouched.value = false
    void focusDialog()
  } else {
    selectedMember.value = ''
    copyState.value = 'idle'
    modeTouched.value = false
  }
})

watch(() => props.loading, (loading, wasLoading) => {
  if (props.open && wasLoading && !loading && !modeTouched.value) initialMode.value = props.mode
})

watch(() => [props.published, props.publication?.mode] as const, ([published, publicationMode]) => {
  if (!props.open || props.loading || !modeTouched.value) return
  if (published && publicationMode === selectedMode.value) {
    initialMode.value = selectedMode.value
    modeTouched.value = false
  }
})

watch(() => props.members, (members) => {
  if (selectedMember.value && !members.some((member) => member.user === selectedMember.value)) {
    selectedMember.value = ''
  }
})

function close() {
  if (props.busy) return
  if (modeTouched.value && selectedMode.value !== initialMode.value) {
    // The parent owns the persisted publication, while this component owns
    // the draft select. Restore the opening value before every close path so
    // Cancel, Escape, and backdrop dismissal cannot leak an unsaved mode.
    emit('update:mode', initialMode.value)
  }
  modeTouched.value = false
  emit('close')
}

function openProductionSettings() {
  if (props.busy) return
  if (modeTouched.value && selectedMode.value !== initialMode.value) {
    emit('update:mode', initialMode.value)
  }
  modeTouched.value = false
  emit('open-production-settings')
}

function addMember() {
  const user = selectedMember.value.trim()
  if (!user || !canAddMember.value) return
  emit('grant', user)
  selectedMember.value = ''
}

function primaryAction() {
  if (!canSave.value) return
  if (modeDirty.value || !props.published) emit('save')
  else close()
}

async function copyLink() {
  if (!link.value) return
  if (await copyTextWithFallback(link.value)) {
    copyState.value = 'copied'
    return
  }
  copyState.value = 'error'
  await nextTick()
  linkInput.value?.focus()
  linkInput.value?.select()
}

function handleKeydown(event: KeyboardEvent) {
  // The shared confirm listener is attached to window, while this dialog's
  // listener is on document. Check shared state directly because document
  // bubbles before window; defaultPrevented is set too late to protect this
  // underlying dialog on Escape.
  if (!props.open || event.defaultPrevented || confirmState.open) return
  if (event.key === 'Escape') {
    close()
    return
  }
  if (event.key !== 'Tab') return
  const focusable = Array.from(dialogRef.value?.querySelectorAll<HTMLElement>(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
  ) ?? [])
  if (focusable.length === 0) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeydown)
  if (props.open) void focusDialog()
})

onBeforeUnmount(() => document.removeEventListener('keydown', handleKeydown))
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-surface/60 p-4 sm:items-center"
      role="presentation"
      @click.self="close"
    >
      <section
        ref="dialogRef"
        class="grid w-full max-w-lg gap-0 overflow-hidden rounded-lg border border-border-default bg-surface-raised shadow-xl"
        role="dialog"
        aria-modal="true"
        :aria-busy="loading"
        aria-labelledby="project-share-dialog-title"
        aria-describedby="project-share-dialog-description"
      >
        <header class="flex items-start justify-between gap-4 border-b border-border-subtle px-5 py-4">
          <div class="min-w-0">
            <h2 id="project-share-dialog-title" class="text-[16px] font-semibold text-text-primary">Share {{ projectName }}</h2>
            <p id="project-share-dialog-description" class="mt-1 text-[12px] leading-5 text-text-muted">Choose who can access the production app.</p>
          </div>
          <button
            ref="dialogCloseButton"
            type="button"
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
            aria-label="Close share dialog"
            :disabled="busy"
            @click="close"
          >
            <X class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </header>

        <div class="grid gap-4 px-5 py-4">
          <div
            v-if="loading"
            class="grid min-h-[28rem] content-start gap-4"
            role="status"
            aria-live="polite"
            aria-busy="true"
            aria-label="Loading sharing settings"
          >
            <div class="flex items-center gap-2 text-[12px] text-text-muted">
              <Loader2 class="h-4 w-4 animate-spin" :stroke-width="1.75" />
              Checking sharing settings…
            </div>
            <div class="grid gap-4" aria-hidden="true">
              <div class="grid gap-2 rounded-md border border-border-subtle bg-surface-overlay p-3">
                <div class="shimmer h-3 w-28 rounded-sm" />
                <div class="shimmer h-8 w-full rounded-md" />
              </div>
              <div class="grid gap-2">
                <div class="shimmer h-3 w-24 rounded-sm" />
                <div class="shimmer h-9 w-full rounded-md" />
              </div>
              <div class="grid gap-3 rounded-md border border-border-subtle bg-surface-overlay p-3">
                <div class="shimmer h-3 w-32 rounded-sm" />
                <div class="shimmer h-3 w-56 rounded-sm" />
                <div class="flex gap-2">
                  <div class="shimmer h-8 min-w-0 flex-1 rounded-md" />
                  <div class="shimmer h-8 w-24 rounded-md" />
                </div>
                <div class="flex gap-2">
                  <div class="shimmer h-8 min-w-0 flex-1 rounded-md" />
                  <div class="shimmer h-8 w-16 rounded-md" />
                </div>
                <div class="shimmer h-8 w-full rounded-md" />
              </div>
              <div class="shimmer h-10 w-full rounded-md" />
            </div>
          </div>

          <div v-else-if="loadState === 'error'" class="grid gap-3 rounded-md border border-danger/30 bg-danger-subtle px-3 py-3 text-[12px] leading-5 text-danger" role="alert">
            <p>{{ loadError || 'Sharing settings could not be loaded.' }}</p>
            <button type="button" class="justify-self-start text-[11px] font-semibold underline underline-offset-2" @click="emit('retry')">Retry</button>
          </div>

          <template v-else>
            <div v-if="loadState === 'partial'" class="grid gap-2 rounded-md border border-warning/30 bg-warning-subtle px-3 py-3 text-[12px] leading-5 text-warning" role="status">
              <p>Some sharing details could not be refreshed. The data that did load is still available.</p>
              <p v-if="loadError || membersError" class="text-[11px]">{{ loadError || membersError }}</p>
              <button type="button" class="justify-self-start text-[11px] font-semibold underline underline-offset-2" @click="emit('retry')">Retry</button>
            </div>
            <div v-if="link" class="grid gap-2 rounded-md border border-border-subtle bg-surface-overlay p-3">
              <div class="flex min-w-0 items-center gap-2">
                <Link2 class="h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" />
                <input
                  ref="linkInput"
                  :value="link"
                  readonly
                  aria-label="Production app link"
                  class="min-w-0 flex-1 truncate border-0 bg-transparent p-0 font-mono text-[12px] text-accent outline-none selection:bg-accent-subtle"
                  @focus="($event.target as HTMLInputElement).select()"
                >
                <a :href="link" target="_blank" rel="noopener noreferrer" class="shrink-0 text-[11px] font-medium text-accent hover:underline">Open</a>
              </div>
            </div>

            <label class="grid gap-1.5" for="project-share-general-access">
              <span class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">General access</span>
              <select
                id="project-share-general-access"
                v-model="selectedMode"
                class="h-9 w-full rounded-md border border-border-subtle bg-surface px-2.5 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
                aria-label="General access"
                :disabled="busy || loading || !publicationStateAvailable"
              >
                <option value="restricted">Restricted</option>
                <option value="public">Anyone with the link</option>
              </select>
            </label>

            <section v-if="showViewers" class="grid gap-3 rounded-md border border-border-subtle bg-surface-overlay p-3" aria-labelledby="project-share-people-title">
              <div>
                <div id="project-share-people-title" class="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-text-muted">
                  <Users class="h-3.5 w-3.5" :stroke-width="1.75" />
                  People with access
                </div>
                <p class="mt-1 text-[12px] leading-5 text-text-muted">Add viewers from the current organization.</p>
              </div>
              <p v-if="membersError" class="rounded-md border border-warning/30 bg-warning-subtle px-2.5 py-2 text-[11px] leading-4 text-warning" role="status">
                Viewer membership could not be refreshed. Existing viewers remain visible. <button type="button" class="font-semibold underline underline-offset-2" @click="emit('retry')">Retry</button>
              </p>
              <p v-if="modeDirty" class="text-[11px] leading-4 text-warning" role="status">Save Restricted access before adding viewers.</p>
              <div class="flex flex-wrap items-center gap-2">
                <select
                  v-model="selectedMember"
                  class="min-w-0 flex-1 rounded-md border border-border-subtle bg-surface px-2.5 py-2 font-mono text-[12px] text-text-primary outline-none focus:border-accent/50"
                  aria-label="Organization member"
                  :disabled="busy || loading || !publicationStateAvailable || !savedRestricted || modeDirty"
                >
                  <option value="">Choose a member</option>
                  <option v-for="member in availableMembers" :key="member.user" :value="member.user">{{ member.user }}</option>
                </select>
                <button
                  type="button"
                  class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent bg-accent/15 px-3 text-[12px] font-semibold text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                  :disabled="!canAddMember"
                  @click="addMember"
                >
                  <Loader2 v-if="grantBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                  {{ grantBusy ? 'Adding viewer…' : 'Add viewer' }}
                </button>
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <input
                  v-model="inviteEmail"
                  type="email"
                  class="min-w-0 flex-1 rounded-md border border-border-subtle bg-surface px-2.5 py-2 font-mono text-[12px] text-text-primary outline-none focus:border-accent/50"
                  placeholder="Invite by email — new users join at first sign-in"
                  aria-label="Invite by email"
                  :disabled="busy || loading || !publicationStateAvailable || !savedRestricted || modeDirty"
                  @keyup.enter="inviteByEmail"
                />
                <button
                  type="button"
                  class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent bg-accent/15 px-3 text-[12px] font-semibold text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                  :disabled="!canInvite"
                  @click="inviteByEmail"
                >
                  <Loader2 v-if="inviteBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                  {{ inviteBusy ? 'Inviting…' : 'Invite' }}
                </button>
              </div>
              <ul v-if="activeGrants.length" class="grid gap-1.5">
                <li v-for="grant in activeGrants" :key="grant.name" class="flex items-center justify-between gap-2 rounded-md border border-border-subtle px-2.5 py-2 text-[12px]">
                  <span class="min-w-0 truncate font-mono text-text-primary">{{ grant.user }}</span>
                  <button
                    type="button"
                    class="shrink-0 text-[11px] font-medium text-danger hover:underline disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="busy || !publicationStateAvailable"
                    @click="emit('revoke', grant.name)"
                  >
                    <Loader2 v-if="revokeBusy(grant.name)" class="mr-1 inline-block h-3.5 w-3.5 animate-spin align-[-0.15em]" :stroke-width="1.75" />
                    {{ revokeBusy(grant.name) ? 'Revoking…' : 'Revoke' }}
                  </button>
                </li>
              </ul>
              <p v-else class="text-[11px] text-text-muted">No viewers added yet.</p>
            </section>

            <p v-if="error" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ error }}</p>
            <p v-if="published && publication?.ready" class="rounded-md border border-success/30 bg-success-subtle px-3 py-2 text-[12px] leading-5 text-success" role="status">
              {{ link ? 'Publication is ready.' : 'Publication is ready; the production link is still being resolved.' }}
            </p>
            <p v-else-if="published && publication?.error" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ publication.error }}</p>
            <div class="flex flex-wrap items-center justify-between gap-2">
              <p v-if="!published && !productionReady" class="text-[11px] leading-4 text-warning" role="status">Deploy the production app before publishing or changing access.</p>
              <button
                type="button"
                class="text-left text-[12px] font-medium text-accent underline decoration-accent/50 underline-offset-2 transition hover:text-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
                :disabled="busy"
                @click="openProductionSettings"
              >
                Production settings
              </button>
            </div>
          </template>
        </div>

        <footer class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle px-5 py-3">
          <button
            v-if="published"
            type="button"
            class="inline-flex h-8 items-center rounded-md border border-danger/40 bg-danger-subtle px-3 text-[12px] font-medium text-danger transition hover:bg-danger/10 disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="busy || !publicationStateAvailable"
            @click="emit('disable')"
          >
            <Loader2 v-if="disableBusy" class="mr-1 h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
            {{ disableBusy ? 'Disabling access…' : 'Disable access' }}
          </button>
          <span v-else />
          <div class="flex items-center gap-2">
            <button
              type="button"
              class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-2.5 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
              aria-label="Copy production link"
              :disabled="!link || busy || loading"
              @click="copyLink"
            >
              <Check v-if="copyState === 'copied'" class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
              <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
              Copy link
            </button>
            <span v-if="copyState === 'copied'" class="text-[11px] text-success" role="status">Link copied.</span>
            <span v-else-if="copyState === 'error'" class="text-[11px] text-danger" role="alert">Select the link above and copy it manually.</span>
            <button
              type="button"
              class="inline-flex h-8 items-center rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="busy"
              @click="close"
            >
              Cancel
            </button>
            <button
              type="button"
              class="inline-flex h-8 items-center gap-1.5 rounded-md bg-accent px-4 text-[12px] font-semibold text-white shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-text-muted disabled:opacity-100 disabled:shadow-none"
              :disabled="!canSave"
              @click="primaryAction"
            >
              <Loader2 v-if="saveBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
              {{ primaryLabel }}
            </button>
          </div>
        </footer>
      </section>
    </div>
  </Teleport>
</template>
