<script setup lang="ts">
import { ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useEscapeKey } from '@/composables/useEscapeKey'
import { UserRound, X, Copy, Check } from 'lucide-vue-next'

const emit = defineEmits<{ close: [] }>()

useEscapeKey(() => emit('close'))

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
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm"
      @click.self="$emit('close')"
    >
      <div class="w-full max-w-md rounded-xl border border-border-subtle bg-surface-raised shadow-2xl">
        <!-- Header -->
        <div class="flex items-center justify-between border-b border-border-subtle bg-surface-overlay/60 px-4 py-2.5">
          <div class="flex items-center gap-2">
            <UserRound class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="font-mono text-[11px] font-semibold tracking-wider text-text-secondary">
              your identity
            </span>
          </div>
          <button
            class="flex h-6 w-6 items-center justify-center rounded-lg text-text-muted transition-colors hover:bg-surface-overlay hover:text-text-secondary"
            @click="$emit('close')"
          >
            <X class="h-3.5 w-3.5" :stroke-width="2" />
          </button>
        </div>

        <div class="p-5">
          <p class="mb-4 text-[12px] text-text-muted">
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
                  class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-surface-overlay hover:text-accent"
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
                  class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-surface-overlay hover:text-accent"
                  title="Copy user ID"
                  @click="copy(auth.user.userId, 'userId')"
                >
                  <Check v-if="copiedField === 'userId'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                  <Copy v-else class="h-3.5 w-3.5" :stroke-width="2" />
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
