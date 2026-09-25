<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X, ShieldCheck, ShieldAlert, Loader2 } from 'lucide-vue-next'
import type { ProviderDTO, AcceptedClaim, HubAccessRequest, AcceptedHubAccess, AcceptedComposition } from '@/stores/providers'

const props = defineProps<{
  provider: ProviderDTO | null
  // The caller's roles decide which hub capabilities they may accept: an
  // org-scoped one needs an org admin, a workspace-scoped one a workspace or
  // org admin. The hub enforces the same rule; this only avoids offering a
  // checkbox that can only fail.
  orgRole?: string
  workspaceRole?: string
  // Enable is a caller-owned write. Keeping pending/error state in the page
  // that owns the request means a failed write can remain retryable without
  // leaving this modal permanently busy or hiding the error behind it.
  busy?: boolean
  error?: string | null
}>()

const emit = defineEmits<{
  cancel: []
  confirm: [accept: AcceptedClaim[], acceptHubAccess: AcceptedHubAccess[], acceptCompositions: AcceptedComposition[]]
}>()

// Both lists below come out of the ONE declaration, provider.requires[], and
// are told apart by a single rule: an entry that NAMES a provider is a
// composition — kinds of that provider this one creates and manages here,
// recorded in the workspace's Grant — and an entry that names none is a claim
// on a platform or core group, accepted on the APIBinding itself. Each is
// flattened to one decision per resource, which is how it is recorded and how
// it can be withdrawn.

// ClaimOption is one platform/core requirement the user decides on.
interface ClaimOption {
  group: string
  resource: string
  verbs: string[]
}

// CompositionOption is the same for a requirement that names a provider; that
// provider is the OWNER of the kind, not the one being enabled.
interface CompositionOption {
  provider: string
  group: string
  resource: string
  verbs: string[]
}

// One boolean per claim, indexed by claim key. Everything under requires is
// tenant-scoped by definition — the provider only ever reaches these kinds
// inside this workspace — so each starts accepted and the user unticks what
// they will not grant.
const accepted = ref<Record<string, boolean>>({})
const dialogRef = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
let previousFocus: HTMLElement | null = null

const dismissLabel = computed(() => props.busy
  ? 'Dismiss provider access dialog; request continues'
  : 'Close provider access dialog')
const dismissTitle = computed(() => props.busy
  ? 'Dismiss dialog; request continues'
  : 'Close dialog')

const claimKey = (c: ClaimOption) => `${c.group}/${c.resource}`
const hubKey = (h: HubAccessRequest) => `${h.capability}/${h.scope}`
const compositionKey = (c: CompositionOption) => `${c.provider}|${c.group}/${c.resource}`

// Managing another provider's objects in this workspace is a workspace
// decision, so a workspace admin is enough and an org admin can always make
// it. The hub enforces the same rule; this only avoids offering a checkbox
// that can only fail.
function canAcceptComposition(): boolean {
  return props.workspaceRole === 'admin' || props.orgRole === 'admin'
}

// A composition that can only read is described as reading. Anything else
// creates or changes objects, and says so — including a "<resource>/<verb>"
// coordinate, which carries no verb list because the call IS the capability.
function compositionLabel(c: CompositionOption): string {
  const readOnly = c.verbs.length > 0 && c.verbs.every((v) => v === 'get' || v === 'list' || v === 'watch')
  const kind = c.resource.charAt(0).toUpperCase() + c.resource.slice(1)
  return readOnly
    ? `Read ${kind} (${c.provider}) in this workspace`
    : `Create and manage ${kind} (${c.provider}) in this workspace`
}

// What to print for a requirement's verbs. A "<resource>/<verb>" coordinate
// declares none — the verb is the capability, and the generated claim spells
// every verb — so calling that "none" would describe it as harmless.
function verbsLabel(c: { resource: string; verbs: string[] }): string {
  if (c.verbs.length) return c.verbs.join(', ')
  return c.resource.includes('/') ? 'the call itself' : 'none'
}

// One boolean per requested hub capability. Those the caller may accept
// start accepted (the provider asks for them to work); the rest are shown
// disabled with who can accept them.
const acceptedHub = ref<Record<string, boolean>>({})

// Same for compositions.
const acceptedComposition = ref<Record<string, boolean>>({})

function canAcceptHub(h: HubAccessRequest): boolean {
  if (h.scope === 'org') return props.orgRole === 'admin'
  return props.workspaceRole === 'admin' || props.orgRole === 'admin'
}

function hubLabel(h: HubAccessRequest): string {
  switch (`${h.capability}/${h.scope}`) {
    case 'memberships.read/org':
      return "Read your organization's member list"
    case 'memberships.read/workspace':
      return "Read this workspace's member list"
    case 'memberships.invite/org':
      return h.allowInvite
        ? 'Add people to your organization as members, inviting them by email'
        : 'Add existing users to your organization as members'
    default:
      return `${h.capability} (${h.scope})`
  }
}

watch(
  () => props.provider,
  (p) => {
    if (!p) return
    const next: Record<string, boolean> = {}
    for (const c of claimsOf(p)) {
      next[claimKey(c)] = true
    }
    accepted.value = next
    const nextHub: Record<string, boolean> = {}
    for (const h of p.hub?.access ?? []) {
      nextHub[hubKey(h)] = canAcceptHub(h)
    }
    acceptedHub.value = nextHub
    const nextComposition: Record<string, boolean> = {}
    for (const c of compositionsOf(p)) {
      nextComposition[compositionKey(c)] = canAcceptComposition()
    }
    acceptedComposition.value = nextComposition
  },
  { immediate: true },
)

const hubAccess = computed(() => props.provider?.hub?.access ?? [])

// The requires[] entries that name no provider: claims on a platform group or
// the core group, which the hub accepts or rejects on the APIBinding itself.
function claimsOf(p: ProviderDTO | null): ClaimOption[] {
  const out: ClaimOption[] = []
  for (const requirement of p?.requires ?? []) {
    if (requirement.provider) continue
    for (const resource of requirement.resources ?? []) {
      out.push({ group: requirement.group ?? '', resource: resource.name, verbs: resource.verbs ?? [] })
    }
  }
  return out
}

// The requires[] entries that DO name a provider: kinds of that provider this
// one manages here, decided by a workspace or org admin and recorded in the
// workspace's Grant.
function compositionsOf(p: ProviderDTO | null): CompositionOption[] {
  const out: CompositionOption[] = []
  for (const requirement of p?.requires ?? []) {
    if (!requirement.provider) continue
    for (const resource of requirement.resources ?? []) {
      out.push({
        provider: requirement.provider,
        group: requirement.group ?? '',
        resource: resource.name,
        verbs: resource.verbs ?? [],
      })
    }
  }
  return out
}

const compositions = computed(() => compositionsOf(props.provider))

function toggleComposition(c: CompositionOption) {
  if (props.busy || !canAcceptComposition()) return
  const k = compositionKey(c)
  acceptedComposition.value = { ...acceptedComposition.value, [k]: !acceptedComposition.value[k] }
}

function toggleHub(h: HubAccessRequest) {
  if (props.busy || !canAcceptHub(h)) return
  const k = hubKey(h)
  acceptedHub.value = { ...acceptedHub.value, [k]: !acceptedHub.value[k] }
}

const claims = computed(() => claimsOf(props.provider))

function toggle(c: ClaimOption) {
  if (props.busy) return
  const k = claimKey(c)
  accepted.value = { ...accepted.value, [k]: !accepted.value[k] }
}

function onConfirm() {
  if (!props.provider || props.busy) return
  const accept = claims.value
    .filter((c) => accepted.value[claimKey(c)])
    .map((c) => ({ group: c.group, resource: c.resource }))
  const acceptHub = hubAccess.value
    .filter((h) => canAcceptHub(h) && acceptedHub.value[hubKey(h)])
    .map((h) => ({ capability: h.capability, scope: h.scope }))
  const acceptCompositions = canAcceptComposition()
    ? compositions.value
        .filter((c) => acceptedComposition.value[compositionKey(c)])
        .map((c) => ({ provider: c.provider, group: c.group, resource: c.resource }))
    : []
  emit('confirm', accept, acceptHub, acceptCompositions)
}

function onKeydown(event: KeyboardEvent) {
  if (!props.provider) return
  if (event.key === 'Escape') {
    event.preventDefault()
    emit('cancel')
    return
  }
  if (event.key !== 'Tab') return
  const focusable = Array.from(dialogRef.value?.querySelectorAll<HTMLElement>(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
  ) ?? [])
  if (!focusable.length) {
    event.preventDefault()
    dialogRef.value?.focus()
    return
  }
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  const activeIndex = focusable.indexOf(document.activeElement as HTMLElement)
  if (event.shiftKey && activeIndex <= 0) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (activeIndex < 0 || activeIndex >= focusable.length - 1)) {
    event.preventDefault()
    first.focus()
  }
}

watch(
  () => !!props.provider,
  (open) => {
    if (open) {
      previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
      window.addEventListener('keydown', onKeydown)
      nextTick(() => closeButton.value?.focus())
    } else {
      window.removeEventListener('keydown', onKeydown)
      const target = previousFocus
      previousFocus = null
      nextTick(() => target?.isConnected && target.focus())
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div
    v-if="provider"
    class="k-modal-overlay"
    @click.self="!busy && $emit('cancel')"
  >
    <div
      ref="dialogRef"
      class="k-modal w-full max-w-lg p-0"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      aria-labelledby="provider-enable-title"
      :aria-describedby="error ? 'provider-enable-description provider-enable-error' : 'provider-enable-description'"
      :aria-busy="busy"
    >
      <div class="flex items-center justify-between border-b border-border-subtle px-4 py-3">
        <div>
          <h2 id="provider-enable-title" class="text-sm font-semibold text-text-primary">Enable {{ provider.displayName }}</h2>
          <p id="provider-enable-description" class="mt-0.5 text-[11px] text-text-muted">
            Review what this provider will be able to access in your workspace.
          </p>
        </div>
        <button ref="closeButton" type="button" class="k-btn k-btn--ghost p-1 text-text-muted hover:text-text-primary" :aria-label="dismissLabel" :title="dismissTitle" @click="$emit('cancel')">
          <X class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>

      <div class="max-h-[60vh] overflow-y-auto px-4 py-3">
        <div
          v-if="error"
          id="provider-enable-error"
          class="mb-3 flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-[11px] text-danger"
          role="alert"
          aria-live="assertive"
        >
          <ShieldAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="2" />
          <span>{{ error }}</span>
        </div>

        <!-- Nothing at all under requires[]: no platform claim and no
             composition. A provider with compositions but no platform claim
             still has something to review, just further down. -->
        <div v-if="claims.length === 0 && compositions.length === 0" class="rounded-lg border border-border-subtle bg-surface-overlay/50 px-3 py-4 text-center text-xs text-text-muted">
          This provider does not request access to any tenant resources.
          Clicking Enable provider will bind its APIs into your workspace.
        </div>

        <ul v-else-if="claims.length" class="space-y-2">
          <li
            v-for="c in claims"
            :key="claimKey(c)"
            class="rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2"
          >
            <label class="k-checkbox-hit flex cursor-pointer items-start gap-3">
              <input
                type="checkbox"
                class="k-checkbox mt-1"
                :checked="!!accepted[claimKey(c)]"
                :disabled="busy"
                @change="toggle(c)"
              />
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <ShieldCheck class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                  <span class="font-mono text-[11px] text-text-primary">
                    {{ c.group ? `${c.group}/` : '' }}{{ c.resource }}
                  </span>
                </div>
                <p class="mt-0.5 text-[10px] text-text-muted">
                  Verbs: <span class="font-mono">{{ verbsLabel(c) }}</span>
                </p>
              </div>
            </label>
          </li>
        </ul>

        <div v-if="compositions.length" class="mt-3">
          <p class="mb-1.5 text-[11px] font-medium text-text-primary">Building on other providers</p>
          <p class="mb-2 text-[10px] text-text-muted">
            {{ provider.displayName }} builds what you ask for out of other providers' resources,
            in this workspace only. It uses a credential the hub issues for each of your objects —
            never a standing one of its own — and what you leave unchecked is declined.
          </p>
          <ul class="space-y-2">
            <li
              v-for="c in compositions"
              :key="compositionKey(c)"
              class="rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2"
            >
              <label class="k-checkbox-hit flex items-start gap-3" :class="canAcceptComposition() ? 'cursor-pointer' : 'cursor-not-allowed opacity-70'">
                <input
                  type="checkbox"
                  class="k-checkbox mt-1"
                  :checked="!!acceptedComposition[compositionKey(c)]"
                  :disabled="busy || !canAcceptComposition()"
                  @change="toggleComposition(c)"
                />
                <div class="min-w-0 flex-1">
                  <span class="text-[11px] text-text-primary">{{ compositionLabel(c) }}</span>
                  <p class="mt-0.5 font-mono text-[10px] text-text-muted">{{ c.group }}/{{ c.resource }}</p>
                  <p class="mt-0.5 text-[10px] text-text-muted">
                    Verbs: <span class="font-mono">{{ verbsLabel(c) }}</span>
                  </p>
                  <p v-if="!canAcceptComposition()" class="mt-1 text-[10px] text-warning">
                    Only a workspace or organization admin can decide this; enabling leaves it as it is.
                  </p>
                </div>
              </label>
            </li>
          </ul>
        </div>

        <div v-if="hubAccess.length" class="mt-3">
          <p class="mb-1.5 text-[11px] font-medium text-text-primary">Acting for you in railgrid</p>
          <p class="mb-2 text-[10px] text-text-muted">
            The provider can do these things as the person using it, and never more than that
            person may. What you leave unchecked is declined; you can change it by enabling the
            provider again.
          </p>
          <ul class="space-y-2">
            <li
              v-for="h in hubAccess"
              :key="hubKey(h)"
              class="rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2"
            >
              <label class="k-checkbox-hit flex items-start gap-3" :class="canAcceptHub(h) ? 'cursor-pointer' : 'cursor-not-allowed opacity-70'">
                <input
                  type="checkbox"
                  class="k-checkbox mt-1"
                  :checked="!!acceptedHub[hubKey(h)]"
                  :disabled="busy || !canAcceptHub(h)"
                  @change="toggleHub(h)"
                />
                <div class="min-w-0 flex-1">
                  <span class="text-[11px] text-text-primary">{{ hubLabel(h) }}</span>
                  <p class="mt-0.5 text-[10px] text-text-muted">{{ h.reason }}</p>
                  <p v-if="h.capability === 'memberships.invite'" class="mt-0.5 text-[10px] text-text-muted">
                    Only as members, never as admins.
                  </p>
                  <p v-if="!canAcceptHub(h)" class="mt-1 text-[10px] text-warning">
                    {{ h.scope === 'org' ? 'Only an organization admin can decide this; enabling leaves it as it is.' : 'Only a workspace or organization admin can decide this; enabling leaves it as it is.' }}
                  </p>
                </div>
              </label>
            </li>
          </ul>
        </div>

      </div>

      <div class="flex items-center justify-end gap-2 border-t border-border-subtle px-4 py-3">
        <button
          type="button"
          class="k-btn k-btn--ghost px-3 py-1 text-[11px] text-text-muted transition-colors hover:text-text-primary"
          :disabled="busy"
          @click="$emit('cancel')"
        >
          Cancel
        </button>
        <button
          type="button"
          class="k-btn k-btn--primary px-3 py-1 text-[11px] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="busy"
          @click="onConfirm"
        >
          <Loader2 v-if="busy" class="h-3 w-3 animate-spin" :stroke-width="2" />
          {{ busy ? 'Enabling provider…' : 'Enable provider' }}
        </button>
      </div>
    </div>
  </div>
</template>
