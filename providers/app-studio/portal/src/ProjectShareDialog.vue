<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, Copy, ExternalLink, Link2, Loader2, MonitorPlay, Rocket, Users, X } from 'lucide-vue-next'
import { copyTextWithFallback } from './clipboard'
import StatusBadge from './portalkit/StatusBadge.vue'
import { confirmDialog, confirmState } from './portalkit/confirm'
import { sharePreviewAccessDraftState } from './sharePreviewAccess'
import type { ProjectPublishingGrant, ProjectPublishingMember, ProjectPublishingMode } from './types'

type ShareLoadState = 'idle' | 'loading' | 'partial' | 'ready' | 'error'
type ShareChannel = 'production' | 'preview'
type StatusTone = 'success' | 'warning' | 'danger' | 'muted'

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
  // The development preview is the second sharing channel. It is only offered
  // when the project's template actually exposes a URL (previewSupported);
  // previewConverged is false while the platform is still applying a
  // just-changed mode, so the dialog says pending instead of claiming the URL
  // already changed hands.
  previewMode?: ProjectPublishingMode
  // API-acknowledged desired mode. Keep this separate from previewMode, which
  // is the editable v-model draft echoed by the parent before any save occurs.
  previewSavedMode?: ProjectPublishingMode
  previewURL?: string
  previewSupported?: boolean
  previewConverged?: boolean
  previewGrants?: ProjectPublishingGrant[]
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
  previewMode: 'restricted',
  previewSavedMode: 'restricted',
  previewURL: '',
  previewSupported: false,
  previewConverged: true,
  previewGrants: () => [],
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
  (event: 'update:previewMode', mode: ProjectPublishingMode): void
  (event: 'save'): void
  (event: 'save-preview'): void
  (event: 'grant', user: string): void
  (event: 'invite', email: string): void
  (event: 'revoke', grant: string): void
  (event: 'preview-grant', user: string): void
  (event: 'preview-invite', email: string): void
  (event: 'preview-revoke', grant: string): void
  (event: 'disable'): void
  (event: 'open-publishing'): void
  (event: 'retry'): void
}>()

const selectedMember = ref('')
const inviteEmail = ref('')
const productionCopyState = ref<'idle' | 'copied' | 'error'>('idle')
const previewCopyState = ref<'idle' | 'copied' | 'error'>('idle')
const initialMode = ref(props.mode)
const modeTouched = ref(false)
const editingChannel = ref<ShareChannel | null>(null)
const dialogCloseButton = ref<HTMLButtonElement | null>(null)
const productionLinkInput = ref<HTMLInputElement | null>(null)
const previewLinkInput = ref<HTMLInputElement | null>(null)
const dialogRef = ref<HTMLElement | null>(null)

const selectedMode = computed({
  get: () => props.mode,
  set: (mode: ProjectPublishingMode) => {
    if (!props.publicationStateAvailable) return
    modeTouched.value = true
    emit('update:mode', mode)
  },
})
const selectedPreviewMode = computed({
  get: () => props.previewMode,
  set: (mode: ProjectPublishingMode) => {
    emit('update:previewMode', mode)
  },
})
const previewLink = computed(() => props.previewURL.trim())
const previewSelectedMember = ref('')
const previewInviteEmail = ref('')
const previewActiveGrants = computed(() => props.previewGrants.filter((grant) => !grant.revoked))
const previewAvailableMembers = computed(() => props.members.filter((member) => (
  !previewActiveGrants.value.some((grant) => grant.user === member.user)
)))
const previewDraftState = computed(() => sharePreviewAccessDraftState(
  selectedPreviewMode.value,
  props.previewSavedMode,
  props.previewSupported,
  props.previewConverged,
))
const previewDirty = computed(() => previewDraftState.value.dirty)
const previewPending = computed(() => previewDraftState.value.pending)
// Same rule as production: grants only make sense on a restricted channel, and
// a draft mode must be saved first so a public preview cannot take a grant.
const previewShowViewers = computed(() => props.previewSupported && selectedPreviewMode.value === 'restricted')
const previewSavedRestricted = computed(() => (
  props.previewSupported && !previewDirty.value && selectedPreviewMode.value === 'restricted'
))
const canAddPreviewMember = computed(() => (
  previewSavedRestricted.value && !!previewSelectedMember.value && !props.busy && !props.loading
))
const previewInviteEmailValid = computed(() => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(previewInviteEmail.value.trim()))
const canInvitePreview = computed(() => (
  previewSavedRestricted.value && previewInviteEmailValid.value && !props.busy && !props.loading
))

function addPreviewMember() {
  const user = previewSelectedMember.value.trim()
  if (!user || !canAddPreviewMember.value) return
  emit('preview-grant', user)
  previewSelectedMember.value = ''
}

function invitePreviewByEmail() {
  const email = previewInviteEmail.value.trim()
  if (!canInvitePreview.value || !email) return
  emit('preview-invite', email)
  previewInviteEmail.value = ''
}

const link = computed(() => props.productionURL.trim() || props.publication?.url?.trim() || '')
const activeGrants = computed(() => props.grants.filter((grant) => !grant.revoked))
const availableMembers = computed(() => props.members.filter((member) => (
  !activeGrants.value.some((grant) => grant.user === member.user)
)))
const showViewers = computed(() => props.publicationStateAvailable && props.published && selectedMode.value === 'restricted')
const modeDirty = computed(() => props.published && selectedMode.value !== initialMode.value)
const productionDraftDirty = computed(() => (
  props.publicationStateAvailable && (props.published || props.productionReady) && selectedMode.value !== initialMode.value
))
const savedRestricted = computed(() => props.publicationStateAvailable && props.published && props.publication?.mode === 'restricted')
// `busy` remains the broad mutation gate so controls cannot issue concurrent
// writes. `busyAction` keeps the visible pending state on the control that
// initiated the write instead of making every mutation look equally active.
const productionSaveBusy = computed(() => props.busy && props.busyAction === 'save')
const previewSaveBusy = computed(() => props.busy && props.busyAction === null && previewDirty.value)
const grantBusy = computed(() => props.busy && props.busyAction === 'grant')
const inviteBusy = computed(() => props.busy && props.busyAction === 'invite')
const disableBusy = computed(() => props.busy && props.busyAction === 'disable')
function revokeBusy(grant: string) {
  return props.busy && props.busyAction === 'revoke' && props.busyTarget === grant
}
const canSaveProduction = computed(() => (
  !props.loading && props.loadState !== 'error' && !props.busy &&
  props.publicationStateAvailable && (props.published || props.productionReady) && (!props.published || productionDraftDirty.value)
))
const canSavePreview = computed(() => (
  !props.loading && props.loadState !== 'error' && !props.busy && previewDirty.value
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

const productionStatus = computed(() => {
  if (!props.published && !props.productionReady) return 'Not deployed'
  if (!props.published) return 'Deployable'
  if (props.publication?.error && !props.publication?.ready) return 'Error'
  return props.publication?.ready ? 'Ready' : 'Pending'
})
const productionStatusTone = computed<StatusTone>(() => {
  if (productionStatus.value === 'Ready') return 'success'
  if (productionStatus.value === 'Pending') return 'warning'
  if (productionStatus.value === 'Error') return 'danger'
  return 'muted'
})
const previewStatus = computed(() => previewPending.value ? 'Pending' : 'Available')
const previewStatusTone = computed<StatusTone>(() => previewPending.value ? 'warning' : 'muted')
const productionAudienceSummary = computed(() => {
  if (!props.published) return 'External access is not enabled'
  if (props.publication?.mode === 'public') return 'Anyone with the link'
  const guestCount = activeGrants.value.length
  return guestCount === 0 ? 'Workspace members only' : `Workspace members + ${guestCount} invited ${guestCount === 1 ? 'person' : 'people'}`
})
const previewAudienceSummary = computed(() => {
  if (props.previewMode === 'public') return 'Anyone with the link'
  const guestCount = previewActiveGrants.value.length
  return guestCount === 0 ? 'Workspace members only' : `Workspace members + ${guestCount} invited ${guestCount === 1 ? 'person' : 'people'}`
})

function defaultEditingChannel(): ShareChannel | null {
  if (props.publicationStateAvailable && (props.published || props.productionReady)) return 'production'
  if (props.previewSupported) return 'preview'
  return null
}

function channelDirty(channel: ShareChannel): boolean {
  return channel === 'production' ? productionDraftDirty.value : previewDirty.value
}

function restoreProductionDraft() {
  if (modeTouched.value && selectedMode.value !== initialMode.value) emit('update:mode', initialMode.value)
  modeTouched.value = false
}

function restorePreviewDraft() {
  if (selectedPreviewMode.value !== props.previewSavedMode) emit('update:previewMode', props.previewSavedMode)
}

function restoreChannelDraft(channel: ShareChannel) {
  if (channel === 'production') restoreProductionDraft()
  else restorePreviewDraft()
}

async function editChannel(channel: ShareChannel) {
  if (props.busy || editingChannel.value === channel) return
  const current = editingChannel.value
  if (current && channelDirty(current)) {
    const confirmed = await confirmDialog({
      title: `Discard unsaved ${current === 'production' ? 'Production' : 'Preview'} access changes?`,
      message: 'The other destination can be edited after you save or discard this draft.',
      confirmLabel: 'Discard changes',
      danger: true,
    })
    if (!confirmed) return
    restoreChannelDraft(current)
  }
  editingChannel.value = channel
}

function cancelChannel(channel: ShareChannel) {
  if (props.busy) return
  restoreChannelDraft(channel)
  editingChannel.value = null
}

function saveProductionAccess() {
  if (!canSaveProduction.value) return
  emit('save')
}

function savePreviewAccess() {
  if (!canSavePreview.value) return
  emit('save-preview')
}

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
    editingChannel.value = props.loading ? null : defaultEditingChannel()
    void focusDialog()
  } else {
    selectedMember.value = ''
    previewSelectedMember.value = ''
    previewInviteEmail.value = ''
    productionCopyState.value = 'idle'
    previewCopyState.value = 'idle'
    modeTouched.value = false
    editingChannel.value = null
  }
})

watch(() => props.loading, (loading, wasLoading) => {
  if (!props.open || !wasLoading || loading) return
  if (!modeTouched.value) initialMode.value = props.mode
  if (!editingChannel.value) editingChannel.value = defaultEditingChannel()
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
  restoreProductionDraft()
  restorePreviewDraft()
  emit('close')
}

function openPublishing() {
  if (props.busy) return
  restoreProductionDraft()
  restorePreviewDraft()
  emit('open-publishing')
}

function addMember() {
  const user = selectedMember.value.trim()
  if (!user || !canAddMember.value) return
  emit('grant', user)
  selectedMember.value = ''
}

async function copyChannelLink(channel: ShareChannel) {
  const value = channel === 'production' ? link.value : previewLink.value
  if (!value) return
  if (await copyTextWithFallback(value)) {
    if (channel === 'production') productionCopyState.value = 'copied'
    else previewCopyState.value = 'copied'
    return
  }
  if (channel === 'production') productionCopyState.value = 'error'
  else previewCopyState.value = 'error'
  await nextTick()
  const input = channel === 'production' ? productionLinkInput.value : previewLinkInput.value
  input?.focus()
  input?.select()
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
  <Teleport to="#app-studio-overlay-root">
    <div
      v-if="open"
      class="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-surface/60 p-4 sm:items-center"
      role="presentation"
      @click.self="close"
    >
      <section
        ref="dialogRef"
        class="grid w-full max-w-xl gap-0 overflow-hidden rounded-lg border border-border-default bg-surface-raised shadow-xl"
        role="dialog"
        aria-modal="true"
        :aria-busy="loading"
        aria-labelledby="project-share-dialog-title"
        aria-describedby="project-share-dialog-description"
      >
        <header class="flex items-start justify-between gap-4 border-b border-border-subtle px-5 py-4">
          <div class="min-w-0">
            <h2 id="project-share-dialog-title" class="text-[16px] font-semibold text-text-primary">Share {{ projectName }}</h2>
            <p id="project-share-dialog-description" class="mt-1 text-[12px] leading-5 text-text-muted">Manage Production and Preview separately.</p>
          </div>
          <button
            ref="dialogCloseButton"
            type="button"
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50"
            aria-label="Close share dialog"
            :disabled="busy"
            @click="close"
          >
            <X class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </header>

        <div class="grid gap-3 px-5 py-4">
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
            <div class="grid gap-3" aria-hidden="true">
              <div v-for="index in 2" :key="index" class="grid gap-3 rounded-lg border border-border-subtle bg-surface p-4">
                <div class="flex items-center gap-3">
                  <div class="shimmer h-7 w-7 rounded-md" />
                  <div class="grid flex-1 gap-2">
                    <div class="shimmer h-3 w-32 rounded-sm" />
                    <div class="shimmer h-3 w-56 max-w-full rounded-sm" />
                  </div>
                </div>
                <div class="shimmer h-8 w-full rounded-md" />
              </div>
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

            <section
              class="overflow-hidden rounded-lg border bg-surface transition-colors"
              :class="editingChannel === 'production' ? 'border-accent/40' : 'border-border-subtle'"
              aria-labelledby="project-share-production-title"
            >
              <div class="flex items-start justify-between gap-3 p-4">
                <div class="flex min-w-0 items-start gap-3">
                  <div class="grid h-7 w-7 shrink-0 place-items-center rounded-md bg-accent-subtle text-accent">
                    <Rocket class="h-4 w-4" :stroke-width="1.75" />
                  </div>
                  <div class="min-w-0">
                    <h3 id="project-share-production-title" class="text-[14px] font-semibold text-text-primary">Production app</h3>
                    <p class="mt-1 text-[12px] leading-4 text-text-muted">Stable release for customers and external users.</p>
                  </div>
                </div>
                <StatusBadge :status="productionStatus" :tone="productionStatusTone" />
              </div>

              <div v-if="!published && !productionReady" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle px-4 py-3">
                <p class="max-w-[42ch] text-[12px] leading-5 text-text-secondary">Deploy Production before choosing its audience or sharing its link.</p>
                <button
                  type="button"
                  class="inline-flex h-9 items-center gap-1.5 rounded-md border border-border-default bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="busy"
                  @click="openPublishing"
                >
                  Set up production
                  <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" />
                </button>
              </div>

              <div v-else-if="editingChannel !== 'production'" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle px-4 py-3 pl-[3.75rem] max-sm:pl-4">
                <div class="min-w-0">
                  <p class="text-[12px] font-medium text-text-secondary">{{ productionAudienceSummary }}</p>
                  <p v-if="link" class="mt-1 truncate font-mono text-[11px] text-text-muted">{{ link }}</p>
                </div>
                <button
                  type="button"
                  class="inline-flex h-9 items-center rounded-md border border-border-default bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                  :disabled="busy"
                  @click="editChannel('production')"
                >
                  Manage production access
                </button>
              </div>

              <div v-else class="grid gap-3 border-t border-border-subtle px-4 pb-4 pt-3 pl-[3.75rem] max-sm:pl-4">
                <div v-if="link" class="grid gap-1.5">
                  <span class="text-[11px] font-medium text-text-secondary">Production link</span>
                  <div class="flex min-w-0 items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2">
                    <Link2 class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
                    <input
                      ref="productionLinkInput"
                      :value="link"
                      readonly
                      aria-label="Production app link"
                      class="min-w-0 flex-1 truncate border-0 bg-transparent p-0 font-mono text-[11px] text-accent outline-none selection:bg-accent-subtle focus-visible:ring-2 focus-visible:ring-accent"
                      @focus="($event.target as HTMLInputElement).select()"
                    >
                    <button type="button" class="inline-flex items-center gap-1 text-[11px] font-medium text-accent hover:text-accent-hover hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" @click="copyChannelLink('production')">
                      <Check v-if="productionCopyState === 'copied'" class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
                      <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                      {{ productionCopyState === 'copied' ? 'Copied' : 'Copy' }}
                    </button>
                    <a :href="link" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 text-[11px] font-medium text-accent hover:text-accent-hover hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent">
                      Open <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" />
                    </a>
                  </div>
                  <p v-if="productionCopyState === 'error'" class="text-[11px] text-danger" role="alert">Select the link and copy it manually.</p>
                </div>

                <fieldset class="grid gap-2" :disabled="busy || !publicationStateAvailable">
                  <legend class="text-[11px] font-medium text-text-secondary">Who can open the Production app?</legend>
                  <div class="grid gap-2 sm:grid-cols-2">
                    <button
                      v-for="option in [{ value: 'restricted', title: 'Workspace + invited people', detail: 'Sign-in required' }, { value: 'public', title: 'Anyone with the link', detail: 'No sign-in required' }]"
                      :key="option.value"
                      type="button"
                      class="flex items-start gap-2 rounded-md border px-3 py-2.5 text-left transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60"
                      :class="selectedMode === option.value ? 'border-accent/60 bg-accent-subtle' : 'border-border-subtle bg-surface hover:bg-surface-hover'"
                      :aria-pressed="selectedMode === option.value"
                      @click="selectedMode = option.value as ProjectPublishingMode"
                    >
                      <span class="mt-0.5 h-3 w-3 shrink-0 rounded-full border" :class="selectedMode === option.value ? 'border-[3px] border-accent bg-surface' : 'border-border-default bg-surface'" />
                      <span class="min-w-0">
                        <span class="block text-[12px] font-medium text-text-primary">{{ option.title }}</span>
                        <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">{{ option.detail }}</span>
                      </span>
                    </button>
                  </div>
                </fieldset>

                <p v-if="selectedMode === 'public'" class="rounded-md bg-warning-subtle px-3 py-2 text-[11px] leading-4 text-warning" role="status">
                  Anyone with the link can use Production without signing in. Existing invitations remain saved if you switch back.
                </p>

                <section v-if="showViewers" class="grid gap-3 border-t border-border-subtle pt-3" aria-labelledby="project-share-people-title">
                  <div class="flex items-center gap-2">
                    <Users class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
                    <div>
                      <div id="project-share-people-title" class="text-[11px] font-medium text-text-secondary">Invited people</div>
                      <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Production access only.</p>
                    </div>
                  </div>
                  <p v-if="membersError" class="rounded-md bg-warning-subtle px-2.5 py-2 text-[11px] leading-4 text-warning" role="status">
                    Viewer membership could not be refreshed. Existing viewers remain visible. <button type="button" class="font-semibold underline underline-offset-2" @click="emit('retry')">Retry</button>
                  </p>
                  <p v-if="modeDirty" class="text-[11px] leading-4 text-warning" role="status">Save Production access before managing people.</p>
                  <div class="flex flex-wrap items-center gap-2">
                    <select v-model="selectedMember" class="k-input min-w-0 flex-1 font-mono text-[12px]" aria-label="Workspace member" :disabled="busy || loading || !publicationStateAvailable || !savedRestricted || modeDirty">
                      <option value="">Choose a Workspace member</option>
                      <option v-for="member in availableMembers" :key="member.user" :value="member.user">{{ member.user }}</option>
                    </select>
                    <button type="button" class="inline-flex h-9 items-center gap-1.5 rounded-md border border-accent bg-accent/15 px-3 text-[12px] font-semibold text-accent transition hover:bg-accent/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="!canAddMember" @click="addMember">
                      <Loader2 v-if="grantBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                      {{ grantBusy ? 'Adding…' : 'Add member' }}
                    </button>
                  </div>
                  <div class="grid gap-1.5">
                    <span class="text-[11px] font-medium text-text-secondary">Invite by email</span>
                    <div class="flex flex-wrap items-center gap-2">
                      <input v-model="inviteEmail" type="email" class="k-input min-w-0 flex-1 font-mono text-[12px]" placeholder="name@company.com" aria-label="Invite by email" :disabled="busy || loading || !publicationStateAvailable || !savedRestricted || modeDirty" @keyup.enter="inviteByEmail">
                      <button type="button" class="inline-flex h-9 items-center gap-1.5 rounded-md border border-accent bg-accent/15 px-3 text-[12px] font-semibold text-accent transition hover:bg-accent/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="!canInvite" @click="inviteByEmail">
                        <Loader2 v-if="inviteBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                        {{ inviteBusy ? 'Inviting…' : 'Send invite' }}
                      </button>
                    </div>
                    <p class="text-[11px] leading-4 text-text-muted">New users join the Workspace at first sign-in.</p>
                  </div>
                  <ul v-if="activeGrants.length" class="grid gap-1.5">
                    <li v-for="grant in activeGrants" :key="grant.name" class="flex items-center justify-between gap-2 rounded-md border border-border-subtle px-2.5 py-2 text-[12px]">
                      <span class="min-w-0 truncate font-mono text-text-primary">{{ grant.user }}</span>
                      <button type="button" class="shrink-0 text-[11px] font-medium text-danger hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50" :disabled="busy || !publicationStateAvailable" @click="emit('revoke', grant.name)">
                        <Loader2 v-if="revokeBusy(grant.name)" class="mr-1 inline-block h-3.5 w-3.5 animate-spin align-[-0.15em]" :stroke-width="1.75" />
                        {{ revokeBusy(grant.name) ? 'Revoking…' : 'Revoke' }}
                      </button>
                    </li>
                  </ul>
                  <p v-else class="text-[11px] text-text-muted">No invited people yet. Workspace members already have access.</p>
                </section>

                <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-3">
                  <button v-if="published" type="button" class="inline-flex h-9 items-center rounded-md border border-danger/40 bg-danger-subtle px-3 text-[12px] font-medium text-danger transition hover:bg-danger/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="busy || !publicationStateAvailable" @click="emit('disable')">
                    <Loader2 v-if="disableBusy" class="mr-1 h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                    {{ disableBusy ? 'Disabling access…' : 'Disable access' }}
                  </button>
                  <span v-else />
                  <div class="flex items-center gap-2">
                    <button type="button" class="inline-flex h-9 items-center rounded-md border border-border-default bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="busy" @click="cancelChannel('production')">Cancel</button>
                    <button type="button" class="inline-flex h-9 items-center gap-1.5 rounded-md bg-accent px-4 text-[12px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-text-muted disabled:opacity-100 disabled:shadow-none" :disabled="!canSaveProduction" @click="saveProductionAccess">
                      <Loader2 v-if="productionSaveBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                      {{ productionSaveBusy ? (published ? 'Saving…' : 'Enabling…') : (published ? 'Save production access' : 'Enable production access') }}
                    </button>
                  </div>
                </div>
              </div>
            </section>

            <section
              v-if="previewSupported"
              class="overflow-hidden rounded-lg border bg-surface transition-colors"
              :class="editingChannel === 'preview' ? 'border-accent/40' : 'border-border-subtle'"
              aria-labelledby="project-share-preview-title"
            >
              <div class="flex items-start justify-between gap-3 p-4">
                <div class="flex min-w-0 items-start gap-3">
                  <div class="grid h-7 w-7 shrink-0 place-items-center rounded-md bg-accent-subtle text-accent">
                    <MonitorPlay class="h-4 w-4" :stroke-width="1.75" />
                  </div>
                  <div class="min-w-0">
                    <h3 id="project-share-preview-title" class="text-[14px] font-semibold text-text-primary">Development preview</h3>
                    <p class="mt-1 text-[12px] leading-4 text-text-muted">Mutable environment for reviewing in-progress work.</p>
                  </div>
                </div>
                <StatusBadge :status="previewStatus" :tone="previewStatusTone" />
              </div>

              <div v-if="editingChannel !== 'preview'" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle px-4 py-3 pl-[3.75rem] max-sm:pl-4">
                <div class="min-w-0">
                  <p class="text-[12px] font-medium text-text-secondary">{{ previewAudienceSummary }}</p>
                  <p v-if="previewLink" class="mt-1 truncate font-mono text-[11px] text-text-muted">{{ previewLink }}</p>
                </div>
                <button type="button" class="inline-flex h-9 items-center rounded-md border border-border-default bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" :disabled="busy" @click="editChannel('preview')">Manage preview access</button>
              </div>

              <div v-else class="grid gap-3 border-t border-border-subtle px-4 pb-4 pt-3 pl-[3.75rem] max-sm:pl-4">
                <div v-if="previewLink" class="grid gap-1.5">
                  <span class="text-[11px] font-medium text-text-secondary">Preview link</span>
                  <div class="flex min-w-0 items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2">
                    <Link2 class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
                    <input ref="previewLinkInput" :value="previewLink" readonly aria-label="Development preview link" class="min-w-0 flex-1 truncate border-0 bg-transparent p-0 font-mono text-[11px] text-accent outline-none selection:bg-accent-subtle focus-visible:ring-2 focus-visible:ring-accent" @focus="($event.target as HTMLInputElement).select()">
                    <button type="button" class="inline-flex items-center gap-1 text-[11px] font-medium text-accent hover:text-accent-hover hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" @click="copyChannelLink('preview')">
                      <Check v-if="previewCopyState === 'copied'" class="h-3.5 w-3.5 text-success" :stroke-width="1.75" />
                      <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                      {{ previewCopyState === 'copied' ? 'Copied' : 'Copy' }}
                    </button>
                    <a :href="previewLink" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 text-[11px] font-medium text-accent hover:text-accent-hover hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent">
                      Open <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" />
                    </a>
                  </div>
                  <p v-if="previewCopyState === 'error'" class="text-[11px] text-danger" role="alert">Select the link and copy it manually.</p>
                </div>

                <fieldset class="grid gap-2" :disabled="busy">
                  <legend class="text-[11px] font-medium text-text-secondary">Who can open this Preview?</legend>
                  <div class="grid gap-2 sm:grid-cols-2">
                    <button
                      v-for="option in [{ value: 'restricted', title: 'Workspace + invited people', detail: 'Best for work in progress' }, { value: 'public', title: 'Anyone with the link', detail: 'No sign-in required' }]"
                      :key="option.value"
                      type="button"
                      class="flex items-start gap-2 rounded-md border px-3 py-2.5 text-left transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60"
                      :class="selectedPreviewMode === option.value ? 'border-accent/60 bg-accent-subtle' : 'border-border-subtle bg-surface hover:bg-surface-hover'"
                      :aria-pressed="selectedPreviewMode === option.value"
                      @click="selectedPreviewMode = option.value as ProjectPublishingMode"
                    >
                      <span class="mt-0.5 h-3 w-3 shrink-0 rounded-full border" :class="selectedPreviewMode === option.value ? 'border-[3px] border-accent bg-surface' : 'border-border-default bg-surface'" />
                      <span class="min-w-0">
                        <span class="block text-[12px] font-medium text-text-primary">{{ option.title }}</span>
                        <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">{{ option.detail }}</span>
                      </span>
                    </button>
                  </div>
                </fieldset>

                <p v-if="selectedPreviewMode === 'public'" class="rounded-md bg-warning-subtle px-3 py-2 text-[11px] leading-4 text-warning" role="status">
                  This Preview is mutable. Anyone with the link could see test data and future changes. Existing invitations remain saved if you switch back.
                </p>
                <p v-if="previewPending" class="text-[11px] leading-4 text-warning" role="status">Applying the new Preview access. The link keeps its previous visibility until the change lands.</p>

                <section v-if="previewShowViewers" class="grid gap-3 border-t border-border-subtle pt-3" aria-labelledby="project-share-preview-people-title">
                  <div class="flex items-center gap-2">
                    <Users class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
                    <div>
                      <div id="project-share-preview-people-title" class="text-[11px] font-medium text-text-secondary">Invited people</div>
                      <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Preview access only.</p>
                    </div>
                  </div>
                  <p v-if="previewDirty" class="text-[11px] leading-4 text-warning" role="status">Save Preview access before managing people.</p>
                  <div class="flex flex-wrap items-center gap-2">
                    <select v-model="previewSelectedMember" class="k-input min-w-0 flex-1 font-mono text-[12px]" aria-label="Workspace member for Preview access" :disabled="busy || loading || !previewSavedRestricted">
                      <option value="">Choose a Workspace member</option>
                      <option v-for="member in previewAvailableMembers" :key="member.user" :value="member.user">{{ member.user }}</option>
                    </select>
                    <button type="button" class="inline-flex h-9 items-center rounded-md border border-accent bg-accent/15 px-3 text-[12px] font-semibold text-accent transition hover:bg-accent/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="!canAddPreviewMember" @click="addPreviewMember">Add member</button>
                  </div>
                  <div class="grid gap-1.5">
                    <span class="text-[11px] font-medium text-text-secondary">Invite by email</span>
                    <div class="flex flex-wrap items-center gap-2">
                      <input v-model="previewInviteEmail" type="email" class="k-input min-w-0 flex-1 font-mono text-[12px]" placeholder="name@company.com" aria-label="Invite by email to the Preview" :disabled="busy || loading || !previewSavedRestricted" @keyup.enter="invitePreviewByEmail">
                      <button type="button" class="inline-flex h-9 items-center rounded-md border border-accent bg-accent/15 px-3 text-[12px] font-semibold text-accent transition hover:bg-accent/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="!canInvitePreview" @click="invitePreviewByEmail">Send invite</button>
                    </div>
                    <p class="text-[11px] leading-4 text-text-muted">New users join the Workspace at first sign-in.</p>
                  </div>
                  <ul v-if="previewActiveGrants.length" class="grid gap-1.5">
                    <li v-for="grant in previewActiveGrants" :key="grant.name" class="flex items-center justify-between gap-2 rounded-md border border-border-subtle px-2.5 py-2 text-[12px]">
                      <span class="min-w-0 truncate font-mono text-text-primary">{{ grant.user }}</span>
                      <button type="button" class="shrink-0 text-[11px] font-medium text-danger hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50" :disabled="busy" @click="emit('preview-revoke', grant.name)">Revoke</button>
                    </li>
                  </ul>
                  <p v-else class="text-[11px] text-text-muted">No invited people yet. Workspace members already have access.</p>
                </section>

                <div class="flex items-center justify-end gap-2 border-t border-border-subtle pt-3">
                  <button type="button" class="inline-flex h-9 items-center rounded-md border border-border-default bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60" :disabled="busy" @click="cancelChannel('preview')">Cancel</button>
                  <button type="button" class="inline-flex h-9 items-center gap-1.5 rounded-md bg-accent px-4 text-[12px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-text-muted disabled:opacity-100 disabled:shadow-none" :disabled="!canSavePreview" @click="savePreviewAccess">
                    <Loader2 v-if="previewSaveBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                    {{ previewSaveBusy ? 'Saving…' : 'Save preview access' }}
                  </button>
                </div>
              </div>
            </section>

            <p v-if="error" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ error }}</p>
            <p v-if="published && publication?.error && !publication?.ready" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ publication.error }}</p>
          </template>
        </div>

        <footer class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle px-5 py-3">
          <p class="text-[11px] leading-4 text-text-muted">Production and Preview access are saved separately.</p>
          <button
            type="button"
            class="inline-flex h-9 items-center rounded-md border border-border-default bg-surface-overlay px-4 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="busy || productionDraftDirty || previewDirty"
            @click="close"
          >
            Done
          </button>
        </footer>
      </section>
    </div>
  </Teleport>
</template>
