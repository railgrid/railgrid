<script setup lang="ts">
import { ref, computed } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useEscapeKey } from '@/composables/useEscapeKey'
import { Terminal, X, Copy, Check, ExternalLink, Download, Package, Code2 } from 'lucide-vue-next'

const emit = defineEmits<{ close: [] }>()

useEscapeKey(() => emit('close'))

const auth = useAuthStore()

type Method = 'binary' | 'krew' | 'source'
const method = ref<Method>('binary')

const methods: { id: Method; label: string; description: string; icon: typeof Download }[] = [
  { id: 'binary', label: 'Binary', description: 'Prebuilt release', icon: Download },
  { id: 'krew', label: 'Krew', description: 'kubectl plugin', icon: Package },
  { id: 'source', label: 'Source', description: 'go install', icon: Code2 },
]

const hubURL = computed(() => window.location.origin)

const binarySnippet = `# Download the latest release for your OS/arch
curl -fsSL https://github.com/faroshq/kedge/releases/latest/download/kubectl-kedge_$(uname -s)_$(uname -m).tar.gz | tar xz

# Move to a directory on your PATH
sudo mv kubectl-kedge /usr/local/bin/kedge
chmod +x /usr/local/bin/kedge

kedge version`

const krewSnippet = `# Requires kubectl + krew (https://krew.sigs.k8s.io)
kubectl krew index add faros https://github.com/faroshq/krew-index.git
kubectl krew install faros/kedge

# Verify
kubectl kedge version`

const sourceSnippet = `# Requires Go 1.22+
go install github.com/faroshq/kedge/cmd/kedge@latest

kedge version`

const installSnippet = computed(() => {
  if (method.value === 'binary') return binarySnippet
  if (method.value === 'krew') return krewSnippet
  return sourceSnippet
})

const cliBinary = computed(() => (method.value === 'krew' ? 'kubectl kedge' : 'kedge'))

const loginSnippet = computed(
  () => `${cliBinary.value} login --hub-url ${hubURL.value}`,
)

const verifySnippet = computed(
  () => `${cliBinary.value} edge list`,
)

const copiedField = ref<string | null>(null)
async function copy(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {}
}

const releasesURL = 'https://github.com/faroshq/kedge/releases/latest'
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm"
      @click.self="$emit('close')"
    >
      <div class="w-full max-w-2xl max-h-[90vh] overflow-y-auto rounded-xl border border-border-subtle bg-surface-raised shadow-2xl">
        <!-- Header (terminal-style) -->
        <div class="flex items-center justify-between border-b border-border-subtle bg-surface-overlay/60 px-4 py-2.5">
          <div class="flex items-center gap-2">
            <div class="flex items-center gap-1.5">
              <div class="h-2.5 w-2.5 rounded-full bg-danger/60" />
              <div class="h-2.5 w-2.5 rounded-full bg-warning/60" />
              <div class="h-2.5 w-2.5 rounded-full bg-success/60" />
            </div>
            <Terminal class="ml-2 h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="font-mono text-[11px] font-semibold tracking-wider text-text-secondary">
              kedge — quickstart
            </span>
          </div>
          <button
            class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-surface-hover hover:text-text-primary"
            @click="$emit('close')"
          >
            <X class="h-3.5 w-3.5" :stroke-width="2" />
          </button>
        </div>

        <div class="space-y-5 p-6">
          <!-- Intro -->
          <div>
            <h2 class="text-[15px] font-bold text-text-primary">Install & log in to the CLI</h2>
            <p class="mt-1 text-[12px] text-text-muted">
              The <span class="font-mono text-text-secondary">kedge</span> CLI talks to this hub at
              <span class="font-mono text-text-secondary">{{ hubURL }}</span>.
              Once installed, log in once and your kubeconfig will be updated automatically.
            </p>
          </div>

          <!-- Step 1: install method tabs -->
          <div>
            <div class="mb-2 flex items-center gap-2">
              <span class="flex h-5 w-5 items-center justify-center rounded-full bg-accent text-[10px] font-bold text-white">
                1
              </span>
              <span class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                Install the CLI
              </span>
            </div>

            <div class="mb-2 flex gap-1.5">
              <button
                v-for="m in methods"
                :key="m.id"
                type="button"
                class="flex flex-1 items-center gap-1.5 rounded-lg border px-2.5 py-2 text-left transition-all"
                :class="
                  method === m.id
                    ? 'border-accent/40 bg-accent/5'
                    : 'border-border-subtle bg-surface-overlay/40 hover:bg-surface-hover'
                "
                @click="method = m.id"
              >
                <component :is="m.icon" class="h-3.5 w-3.5 shrink-0" :class="method === m.id ? 'text-accent' : 'text-text-muted'" :stroke-width="1.75" />
                <span class="min-w-0">
                  <span class="block text-[11px] font-semibold text-text-primary">{{ m.label }}</span>
                  <span class="block truncate text-[10px] text-text-muted">{{ m.description }}</span>
                </span>
              </button>
            </div>

            <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3">
              <div class="mb-1.5 flex items-center justify-between">
                <span class="font-mono text-[9px] uppercase tracking-[0.2em] text-text-muted/70">$ shell</span>
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copy(installSnippet, 'install')"
                >
                  <component :is="copiedField === 'install' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'install' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ installSnippet }}</pre>
            </div>

            <a
              v-if="method === 'binary'"
              :href="releasesURL"
              target="_blank"
              rel="noopener"
              class="mt-2 inline-flex items-center gap-1 text-[11px] font-medium text-accent transition-colors hover:text-accent-hover"
            >
              Browse releases on GitHub
              <ExternalLink class="h-3 w-3" :stroke-width="1.75" />
            </a>
          </div>

          <!-- Step 2: login -->
          <div>
            <div class="mb-2 flex items-center gap-2">
              <span class="flex h-5 w-5 items-center justify-center rounded-full bg-accent text-[10px] font-bold text-white">
                2
              </span>
              <span class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                Log in to this hub
              </span>
            </div>

            <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3">
              <div class="mb-1.5 flex items-center justify-between">
                <span class="font-mono text-[9px] uppercase tracking-[0.2em] text-text-muted/70">$ shell</span>
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copy(loginSnippet, 'login')"
                >
                  <component :is="copiedField === 'login' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'login' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ loginSnippet }}</pre>
            </div>

            <p class="mt-2 text-[11px] text-text-muted">
              A browser window opens for SSO; the CLI then writes a context named
              <span class="font-mono text-text-secondary">kedge</span> into
              <span class="font-mono text-text-secondary">~/.kube/config</span>.
            </p>
          </div>

          <!-- Step 3: verify -->
          <div>
            <div class="mb-2 flex items-center gap-2">
              <span class="flex h-5 w-5 items-center justify-center rounded-full bg-accent text-[10px] font-bold text-white">
                3
              </span>
              <span class="text-[11px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                Verify
              </span>
            </div>

            <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3">
              <div class="mb-1.5 flex items-center justify-between">
                <span class="font-mono text-[9px] uppercase tracking-[0.2em] text-text-muted/70">$ shell</span>
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
                  @click="copy(verifySnippet, 'verify')"
                >
                  <component :is="copiedField === 'verify' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
                  {{ copiedField === 'verify' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ verifySnippet }}</pre>
            </div>
          </div>

          <!-- Footer note for clusterName context -->
          <div v-if="auth.clusterName" class="rounded-xl border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[10px] text-text-muted">
            Logged in as <span class="font-mono text-text-secondary">{{ auth.user?.email }}</span>
            · workspace <span class="font-mono text-text-secondary">{{ auth.clusterName }}</span>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
