<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X, ShieldCheck, ShieldAlert, Loader2 } from 'lucide-vue-next'
import type { ProviderDTO, PermissionClaim, HubAccessRequest, AcceptedHubAccess } from '@/stores/providers'

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
  confirm: [accept: PermissionClaim[], acceptHubAccess: AcceptedHubAccess[]]
}>()

// One boolean per claim, indexed by claim key. tenantScoped claims default
// to accepted; non-tenantScoped default to rejected so the user has to
// explicitly opt-in to anything that escapes their workspace.
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

const claimKey = (c: PermissionClaim) => `${c.group ?? ''}/${c.resource}`
const hubKey = (h: HubAccessRequest) => `${h.capability}/${h.scope}`

// One boolean per requested hub capability. Those the caller may accept
// start accepted (the provider asks for them to work); the rest are shown
// disabled with who can accept them.
const acceptedHub = ref<Record<string, boolean>>({})

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
    for (const c of p.permissionClaims ?? []) {
      next[claimKey(c)] = !!c.tenantScoped
    }
    accepted.value = next
    const nextHub: Record<string, boolean> = {}
    for (const h of p.hubAccess ?? []) {
      nextHub[hubKey(h)] = canAcceptHub(h)
    }
    acceptedHub.value = nextHub
  },
  { immediate: true },
)

const hubAccess = computed(() => props.provider?.hubAccess ?? [])

function toggleHub(h: HubAccessRequest) {
  if (props.busy || !canAcceptHub(h)) return
  const k = hubKey(h)
  acceptedHub.value = { ...acceptedHub.value, [k]: !acceptedHub.value[k] }
}

const claims = computed(() => props.provider?.permissionClaims ?? [])
const hasUntrustedAccepted = computed(() =>
  claims.value.some((c) => !c.tenantScoped && accepted.value[claimKey(c)]),
)

function toggle(c: PermissionClaim) {
  if (props.busy) return
  const k = claimKey(c)
  accepted.value = { ...accepted.value, [k]: !accepted.value[k] }
}

function onConfirm() {
  if (!props.provider || props.busy) return
  const accept = claims.value.filter((c) => accepted.value[claimKey(c)])
  const acceptHub = hubAccess.value
    .filter((h) => canAcceptHub(h) && acceptedHub.value[hubKey(h)])
    .map((h) => ({ capability: h.capability, scope: h.scope }))
  emit('confirm', accept, acceptHub)
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

        <div v-if="claims.length === 0" class="rounded-lg border border-border-subtle bg-surface-overlay/50 px-3 py-4 text-center text-xs text-text-muted">
          This provider does not request access to any tenant resources.
          Clicking Enable provider will bind its APIs into your workspace.
        </div>

        <ul v-else class="space-y-2">
          <li
            v-for="c in claims"
            :key="claimKey(c)"
            class="rounded-lg border bg-surface-overlay/30 px-3 py-2"
            :class="c.tenantScoped ? 'border-border-subtle' : 'border-warning/30'"
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
                  <ShieldCheck v-if="c.tenantScoped" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                  <ShieldAlert v-else class="h-3.5 w-3.5 text-warning" :stroke-width="2" />
                  <span class="font-mono text-[11px] text-text-primary">
                    {{ c.group ? `${c.group}/` : '' }}{{ c.resource }}
                  </span>
                </div>
                <p class="mt-0.5 text-[10px] text-text-muted">
                  Verbs: <span class="font-mono">{{ (c.verbs ?? []).join(', ') || 'none' }}</span>
                </p>
                <p v-if="!c.tenantScoped" class="mt-1 text-[10px] text-warning">
                  Not marked tenant-scoped — provider could reach beyond your workspace.
                  Only accept if you trust the chart vendor.
                </p>
              </div>
            </label>
          </li>
        </ul>

        <div
          v-if="provider.edgeProxyAccess"
          class="mt-3 rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2"
        >
          <div class="flex items-center gap-2">
            <ShieldCheck class="h-3.5 w-3.5 text-success" :stroke-width="2" />
            <span class="text-[11px] font-medium text-text-primary">Edge cluster access</span>
          </div>
          <p class="mt-0.5 text-[10px] text-text-muted">
            This provider will get proxied read access to the edge clusters
            connected to this workspace (background connections through the
            hub's edges-proxy). Removed when you disable the provider.
          </p>
        </div>

        <div v-if="hubAccess.length" class="mt-3">
          <p class="mb-1.5 text-[11px] font-medium text-text-primary">Acting for you in faros</p>
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

        <div v-if="hasUntrustedAccepted" class="mt-3 rounded-md border border-warning/30 bg-warning-subtle px-3 py-2 text-[11px] text-warning">
          You've accepted at least one claim that isn't tenant-scoped. The
          provider's controllers will be able to read or write the indicated
          resources cluster-wide subject to its MaximalPermissionPolicy.
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
