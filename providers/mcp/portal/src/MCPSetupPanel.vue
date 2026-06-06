<script setup lang="ts">
import { computed, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { Bot, Check, Copy, Monitor, Terminal } from 'lucide-vue-next'

const props = defineProps<{
  serverName: string
  url: string
  embedded?: boolean
}>()

const auth = useAuthStore()

type SetupClient = 'claude-code' | 'claude-desktop' | 'codex'

interface SetupClientOption {
  id: SetupClient
  label: string
  description: string
  icon: typeof Terminal
  snippetLabel: string
}

const clients: SetupClientOption[] = [
  {
    id: 'claude-code',
    label: 'Claude Code',
    description: 'CLI command',
    icon: Terminal,
    snippetLabel: '$ shell',
  },
  {
    id: 'claude-desktop',
    label: 'Claude Desktop',
    description: 'desktop config',
    icon: Monitor,
    snippetLabel: 'claude_desktop_config.json',
  },
  {
    id: 'codex',
    label: 'Codex',
    description: 'CLI command',
    icon: Bot,
    snippetLabel: '$ shell',
  },
]

const selectedClient = ref<SetupClient>('claude-code')
const copied = ref(false)
const codexTokenEnvVar = 'KEDGE_MCP_TOKEN'
const displayToken = '<token>'

const selectedOption = computed(
  () => clients.find((client) => client.id === selectedClient.value) ?? clients[0],
)

function shellSingleQuote(value: string) {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

function buildClaudeCodeSnippet(token: string) {
  return `claude mcp add --transport http ${props.serverName} ${shellSingleQuote(props.url)} \\
  -H ${shellSingleQuote(`Authorization: Bearer ${token}`)}`
}

function buildClaudeDesktopSnippet(token: string) {
  return JSON.stringify(
    {
      mcpServers: {
        [props.serverName]: {
          url: props.url,
          headers: { Authorization: `Bearer ${token}` },
        },
      },
    },
    null,
    2,
  )
}

function buildCodexSnippet(token: string) {
  return `export ${codexTokenEnvVar}=${shellSingleQuote(token)}
codex mcp add ${props.serverName} \\
  --url ${shellSingleQuote(props.url)} \\
  --bearer-token-env-var ${codexTokenEnvVar}`
}

function buildSnippet(client: SetupClient, token: string) {
  if (client === 'claude-desktop') return buildClaudeDesktopSnippet(token)
  if (client === 'codex') return buildCodexSnippet(token)
  return buildClaudeCodeSnippet(token)
}

const displaySnippet = computed(() => buildSnippet(selectedClient.value, displayToken))

async function copySelectedSnippet() {
  try {
    const token = await auth.getValidToken()
    await navigator.clipboard.writeText(buildSnippet(selectedClient.value, token))
    copied.value = true
    setTimeout(() => (copied.value = false), 2000)
  } catch {
    // Clipboard failures are non-fatal; the visible snippet remains usable.
  }
}
</script>

<template>
  <div
    :class="
      embedded
        ? 'space-y-3'
        : 'rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur'
    "
  >
    <div class="mb-3 flex items-center justify-between gap-3">
      <span class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
        Connect this MCP server
      </span>
      <button
        type="button"
        class="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
        @click="copySelectedSnippet"
      >
        <component :is="copied ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
        {{ copied ? 'Copied' : 'Copy' }}
      </button>
    </div>

    <div role="tablist" aria-label="MCP client" class="mb-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
      <button
        v-for="client in clients"
        :key="client.id"
        type="button"
        role="tab"
        :aria-selected="selectedClient === client.id"
        class="flex items-center gap-2 rounded-lg border px-2.5 py-2 text-left transition-all"
        :class="
          selectedClient === client.id
            ? 'border-accent/40 bg-accent/5'
            : 'border-border-subtle bg-surface-overlay/40 hover:bg-surface-hover'
        "
        @click="selectedClient = client.id"
      >
        <component
          :is="client.icon"
          class="h-3.5 w-3.5 shrink-0"
          :class="selectedClient === client.id ? 'text-accent' : 'text-text-muted'"
          :stroke-width="1.75"
        />
        <span class="min-w-0">
          <span class="block text-[11px] font-semibold text-text-primary">{{ client.label }}</span>
          <span class="block truncate text-[10px] text-text-muted">{{ client.description }}</span>
        </span>
      </button>
    </div>

    <div class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3">
      <div class="mb-1.5 flex items-center justify-between">
        <span class="font-mono text-[9px] uppercase tracking-[0.2em] text-text-muted/70">
          {{ selectedOption.snippetLabel }}
        </span>
      </div>
      <pre class="overflow-x-auto rounded-lg bg-surface/80 p-3 font-mono text-[11px] leading-relaxed text-text-secondary">{{ displaySnippet }}</pre>
    </div>
  </div>
</template>
