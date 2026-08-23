<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useEscapeKey } from '@/composables/useEscapeKey'
import { UserRound, X, Copy, Check, LogIn } from 'lucide-vue-next'

const emit = defineEmits<{ close: []; switch: [] }>()

useEscapeKey(() => emit('close'))

const dialogRef = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
let previousFocus: HTMLElement | null = null

function onKeydown(event: KeyboardEvent) {
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

onMounted(() => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  window.addEventListener('keydown', onKeydown)
  nextTick(() => closeButton.value?.focus())
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  const target = previousFocus
  previousFocus = null
  nextTick(() => target?.isConnected && target.focus())
})

const auth = useAuthStore()

// Fields someone can hand out when a workspace/org admin asks "what's
// your UUID/email to add you?". Either the email or the user ID resolves
// server-side (see restapi.resolveUser), so we surface both with copy.
const copiedField = ref<string | null>(null)
async function copy(text: string, field: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}
</script>

<template>
  <Teleport to="body">
    <div
      class="k-modal-overlay"
      @click.self="$emit('close')"
    >
      <div ref="dialogRef" class="k-modal w-full max-w-md p-0" role="dialog" aria-modal="true" aria-labelledby="user-profile-title" aria-describedby="user-profile-description">
        <!-- Header -->
        <div class="flex items-center justify-between border-b border-border-subtle bg-surface-overlay/60 px-4 py-2.5">
          <div class="flex items-center gap-2">
            <UserRound class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span id="user-profile-title" class="font-mono text-[11px] font-semibold tracking-wider text-text-secondary">
              your identity
            </span>
          </div>
          <button
            ref="closeButton"
            type="button"
            class="k-btn k-btn--ghost h-6 w-6 p-0 text-text-muted transition-colors hover:text-text-secondary"
            aria-label="Close profile dialog"
            @click="$emit('close')"
          >
            <X class="h-3.5 w-3.5" :stroke-width="2" />
          </button>
        </div>

        <div class="p-5">
          <p id="user-profile-description" class="mb-4 text-[12px] text-text-muted">
            Share either of these when someone needs to add you to their organization or workspace.
          </p>

          <div class="space-y-3">
            <!-- Email -->
            <div>
              <div class="mb-1 text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                Email
              </div>
              <div class="flex items-center gap-2 rounded-lg border border-border-subtle bg-surface-overlay/50 px-3 py-2">
                <span class="flex-1 truncate font-mono text-[12px] text-text-primary">
                  {{ auth.user?.email || '—' }}
                </span>
                <button
                  v-if="auth.user?.email"
                  type="button"
                  class="k-btn k-btn--ghost flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md p-0 text-text-muted transition-colors hover:text-accent"
                  title="Copy email"
                  @click="copy(auth.user.email, 'email')"
                >
                  <Check v-if="copiedField === 'email'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                  <Copy v-else class="h-3.5 w-3.5" :stroke-width="2" />
                </button>
              </div>
            </div>

            <!-- User ID -->
            <div>
              <div class="mb-1 text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                User ID
              </div>
              <div class="flex items-center gap-2 rounded-lg border border-border-subtle bg-surface-overlay/50 px-3 py-2">
                <span class="flex-1 truncate font-mono text-[12px] text-text-primary">
                  {{ auth.user?.userId || '—' }}
                </span>
                <button
                  v-if="auth.user?.userId"
                  type="button"
                  class="k-btn k-btn--ghost flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md p-0 text-text-muted transition-colors hover:text-accent"
                  title="Copy user ID"
                  @click="copy(auth.user.userId, 'userId')"
                >
                  <Check v-if="copiedField === 'userId'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                  <Copy v-else class="h-3.5 w-3.5" :stroke-width="2" />
                </button>
              </div>
            </div>
          </div>

          <button
            type="button"
            class="k-btn k-btn--ghost mt-5 w-full px-3 py-2 text-[11px] text-text-secondary transition-colors hover:text-accent"
            @click="emit('switch')"
          >
            <LogIn class="h-3.5 w-3.5" :stroke-width="1.75" />
            Switch account
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
