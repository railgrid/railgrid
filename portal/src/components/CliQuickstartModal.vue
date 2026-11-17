<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { X, Copy, Check, ExternalLink } from 'lucide-vue-next'

const emit = defineEmits<{ close: [] }>()

type Step = 'install' | 'login' | 'verify'

const dialogRef = ref<HTMLDialogElement | null>(null)
const copiedField = ref<Step | null>(null)
const copyError = ref<Step | null>(null)
let copyResetTimer: ReturnType<typeof setTimeout> | null = null

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Tab') return
  const focusable = Array.from(dialogRef.value?.querySelectorAll<HTMLElement>(
    'button:not([disabled]), a[href]',
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
  // Let the account menu return focus to its trigger before opening the dialog.
  nextTick(() => dialogRef.value?.showModal())
})

onBeforeUnmount(() => {
  dialogRef.value?.close()
  if (copyResetTimer) clearTimeout(copyResetTimer)
})

const auth = useAuthStore()

type Method = 'binary' | 'krew' | 'source'
const method = ref<Method>('binary')

const methods: { id: Method; label: string }[] = [
  { id: 'binary', label: 'Binary' },
  { id: 'krew', label: 'Krew' },
  { id: 'source', label: 'Source' },
]

const hubURL = computed(() => window.location.origin)

const binarySnippet = `# Installs the latest release to ~/.local/bin (Linux, macOS)
curl -fsSL https://downloads.railgrid.ai/install.sh | sh

railgrid version`

const krewSnippet = `# Requires kubectl + krew (https://krew.sigs.k8s.io)
kubectl krew index add railgrid https://github.com/railgrid/krew-index.git
kubectl krew install railgrid/railgrid

# Verify
kubectl railgrid version`

const sourceSnippet = `# Requires Go 1.22+
go install github.com/railgrid/railgrid/cmd/railgrid@latest

railgrid version`

const installSnippet = computed(() => {
  if (method.value === 'binary') return binarySnippet
  if (method.value === 'krew') return krewSnippet
  return sourceSnippet
})

const cliBinary = computed(() => (method.value === 'krew' ? 'kubectl railgrid' : 'railgrid'))

const loginSnippet = computed(
  () => `${cliBinary.value} login --hub-url ${hubURL.value}`,
)

const verifySnippet = computed(
  () => `${cliBinary.value} edge list`,
)

const steps = computed(() => [
  { id: 'install', title: 'Install', command: installSnippet.value },
  { id: 'login', title: 'Log in', command: loginSnippet.value },
  { id: 'verify', title: 'Verify access', command: verifySnippet.value },
] as const)

function selectMethod(id: Method) {
  if (method.value === id) return
  method.value = id
  copiedField.value = null
  copyError.value = null
}

async function copy(text: string, field: Step) {
  copyError.value = null
  const selectedMethod = method.value
  try {
    if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
    await navigator.clipboard.writeText(text)
    if (selectedMethod !== method.value) return
    copiedField.value = field
    if (copyResetTimer) clearTimeout(copyResetTimer)
    copyResetTimer = setTimeout(() => {
      if (copiedField.value === field) copiedField.value = null
    }, 2000)
  } catch {
    if (selectedMethod === method.value) copyError.value = field
  }
}

const releasesURL = 'https://github.com/railgrid/railgrid/releases/latest'
</script>

<template>
  <Teleport to="body">
    <dialog
      ref="dialogRef"
      class="k-modal-overlay cli-dialog"
      aria-labelledby="cli-quickstart-title"
      aria-describedby="cli-quickstart-description"
      @click.self="emit('close')"
      @cancel.prevent="emit('close')"
      @keydown="onKeydown"
    >
      <div class="k-modal cli-dialog-panel">
        <header class="mb-5 flex items-start justify-between gap-3">
          <div>
            <h2 id="cli-quickstart-title" class="k-modal__title cli-dialog-title">Install the Railgrid CLI</h2>
            <p id="cli-quickstart-description" class="k-modal__message mb-0 text-[13px]">
              Install the CLI, then connect it to this hub.
            </p>
          </div>
          <button
            type="button"
            class="k-btn k-btn--ghost cli-dialog-close"
            aria-label="Close CLI quickstart dialog"
            autofocus
            @click="emit('close')"
          >
            <X class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
          </button>
        </header>

        <div class="divide-y divide-border-subtle">
          <section
            v-for="(step, index) in steps"
            :key="step.id"
            :aria-labelledby="'cli-step-' + step.id"
            class="space-y-2 py-4 first:pt-0 last:pb-0"
          >
            <h3 :id="'cli-step-' + step.id" class="text-[15px] font-semibold">
              {{ index + 1 }}. {{ step.title }}
            </h3>

            <div v-if="step.id === 'install'" class="k-tabs k-tabs--secondary cli-methods" role="group" aria-label="Installation method">
              <button
                v-for="option in methods"
                :key="option.id"
                type="button"
                class="k-tab"
                :class="{ 'k-tab--active': method === option.id }"
                :aria-pressed="method === option.id"
                @click="selectMethod(option.id)"
              >
                {{ option.label }}
              </button>
            </div>

            <div class="flex min-w-0 items-start gap-1 rounded-md border border-border-default bg-surface-overlay/60 p-1">
              <pre class="min-w-0 flex-1 whitespace-pre-wrap px-2 py-2 font-mono text-xs leading-relaxed text-text-primary [overflow-wrap:anywhere]"><code>{{ step.command }}</code></pre>
              <button
                type="button"
                class="k-btn k-btn--ghost cli-copy"
                :aria-label="(copiedField === step.id ? 'Copied ' : 'Copy ') + step.id + ' command'"
                @click="copy(step.command, step.id)"
              >
                <component :is="copiedField === step.id ? Check : Copy" class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                {{ copiedField === step.id ? 'Copied' : 'Copy' }}
              </button>
            </div>

            <p v-if="copyError === step.id" class="text-[13px] text-warning" role="status">
              Copy failed. Select the command and copy it manually.
            </p>

            <a
              v-if="step.id === 'install' && method === 'binary'"
              :href="releasesURL"
              target="_blank"
              rel="noopener"
              class="k-back-action inline-flex items-center"
            >
              Browse releases on GitHub
              <ExternalLink :stroke-width="1.75" aria-hidden="true" />
            </a>

            <p v-if="step.id === 'login'" class="text-[13px] text-text-secondary">
              Your browser opens for SSO. The CLI adds a <code class="font-mono">railgrid</code> context to
              <code class="font-mono">~/.kube/config</code>.
            </p>
          </section>
        </div>

        <p class="sr-only" role="status">{{ copiedField ? 'Copied ' + copiedField + ' command.' : '' }}</p>
        <p v-if="auth.clusterName" class="mt-4 break-words border-t border-border-subtle pt-3 text-xs text-text-secondary">
          Logged in as <span class="font-mono">{{ auth.memberId || auth.user?.userId }}</span>
          · workspace <span class="font-mono">{{ auth.clusterName }}</span>
        </p>
      </div>
    </dialog>
  </Teleport>
</template>

<style scoped>
.cli-dialog {
  padding: 12px;
}

.cli-dialog-panel {
  width: min(100%, 42rem);
  max-height: calc(100dvh - 24px);
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 16px;
}

.cli-dialog-title {
  font-size: 20px;
}

.cli-dialog-close {
  flex: none;
  width: 32px;
  height: 32px;
  padding: 0;
}

.cli-methods {
  flex-wrap: wrap;
  overflow: visible;
}

.cli-copy {
  flex: none;
  width: 76px;
  gap: 6px;
  padding: 6px 8px;
  font-size: 12px;
}

@media (min-width: 640px) {
  .cli-dialog {
    padding: 24px;
  }

  .cli-dialog-panel {
    max-height: calc(100dvh - 48px);
    padding: 24px;
  }
}
</style>
