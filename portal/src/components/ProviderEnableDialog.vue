<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X, ShieldCheck, ShieldAlert, Loader2 } from 'lucide-vue-next'
import type { ProviderDTO, PermissionClaim } from '@/stores/providers'

const props = defineProps<{
  provider: ProviderDTO | null
}>()

const emit = defineEmits<{
  cancel: []
  confirm: [accept: PermissionClaim[]]
}>()

// One boolean per claim, indexed by claim key. tenantScoped claims default
// to accepted; non-tenantScoped default to rejected so the user has to
// explicitly opt-in to anything that escapes their workspace.
const accepted = ref<Record<string, boolean>>({})
const busy = ref(false)
const dialogRef = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
let previousFocus: HTMLElement | null = null

const claimKey = (c: PermissionClaim) => `${c.group ?? ''}/${c.resource}`

watch(
  () => props.provider,
  (p) => {
    if (!p) return
    const next: Record<string, boolean> = {}
    for (const c of p.permissionClaims ?? []) {
      next[claimKey(c)] = !!c.tenantScoped
    }
    accepted.value = next
    busy.value = false
  },
  { immediate: true },
)

const claims = computed(() => props.provider?.permissionClaims ?? [])
const hasUntrustedAccepted = computed(() =>
  claims.value.some((c) => !c.tenantScoped && accepted.value[claimKey(c)]),
)

function toggle(c: PermissionClaim) {
  const k = claimKey(c)
  accepted.value = { ...accepted.value, [k]: !accepted.value[k] }
}

function onConfirm() {
  if (!props.provider) return
  busy.value = true
  const accept = claims.value.filter((c) => accepted.value[claimKey(c)])
  emit('confirm', accept)
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
  if (!focusable.length) return
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
)

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div
    v-if="provider"
    class="k-modal-overlay"
    @click.self="$emit('cancel')"
  >
    <div ref="dialogRef" class="k-modal w-full max-w-lg p-0" role="dialog" aria-modal="true" aria-labelledby="provider-enable-title" aria-describedby="provider-enable-description">
      <div class="flex items-center justify-between border-b border-border-subtle px-4 py-3">
        <div>
          <h2 id="provider-enable-title" class="text-sm font-semibold text-text-primary">Enable {{ provider.displayName }}</h2>
          <p id="provider-enable-description" class="mt-0.5 text-[11px] text-text-muted">
            Review what this provider will be able to access in your workspace.
          </p>
        </div>
        <button ref="closeButton" type="button" class="k-btn k-btn--ghost p-1 text-text-muted hover:text-text-primary" aria-label="Close provider access dialog" @click="$emit('cancel')">
          <X class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>

      <div class="max-h-[60vh] overflow-y-auto px-4 py-3">
        <div v-if="claims.length === 0" class="rounded-lg border border-border-subtle bg-surface-overlay/50 px-3 py-4 text-center text-xs text-text-muted">
          This provider does not request access to any tenant resources.
          Clicking Confirm will bind its APIs into your workspace.
        </div>

        <ul v-else class="space-y-2">
          <li
            v-for="c in claims"
            :key="claimKey(c)"
            class="rounded-lg border bg-surface-overlay/30 px-3 py-2"
            :class="c.tenantScoped ? 'border-border-subtle' : 'border-warning/30'"
          >
            <label class="flex cursor-pointer items-start gap-3">
              <input
                type="checkbox"
                class="k-checkbox mt-1"
                :checked="!!accepted[claimKey(c)]"
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
          Confirm &amp; Enable
        </button>
      </div>
    </div>
  </div>
</template>
