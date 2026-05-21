<script setup lang="ts">
import { useEscapeKey } from '@/composables/useEscapeKey'
import { Bot, X, Terminal, Layers, HelpCircle, Lightbulb } from 'lucide-vue-next'

const emit = defineEmits<{ close: [] }>()

useEscapeKey(() => emit('close'))

interface KindCard {
  id: 'aggregate' | 'kubernetes' | 'linux'
  icon: typeof Bot
  title: string
  tagline: string
  recommended?: boolean
  accent: 'success' | 'accent' | 'warning'
  bestFor: string
  exposes: string[]
  pickWhen: string
  pathHint: string
}

const cards: KindCard[] = [
  {
    id: 'aggregate',
    icon: Layers,
    title: 'Aggregate MCP',
    tagline: 'One endpoint, every edge',
    recommended: true,
    accent: 'success',
    bestFor:
      'You want a single MCP URL covering all your Kubernetes clusters and Linux servers. The AI decides which target to act on.',
    exposes: [
      'Every Kubernetes toolset (kubectl-style read/exec) across all kube edges',
      'Every Linux toolset (SSH exec / fs / systemd / pkg) across all server edges',
      'A list_targets discovery tool the model calls first to see what is available',
    ],
    pickWhen:
      "You're wiring kedge into Claude Code / Desktop once and want everything reachable from there.",
    pathHint: '/services/mcpserver/...',
  },
  {
    id: 'kubernetes',
    icon: Bot,
    title: 'Kubernetes MCP',
    tagline: 'kubectl-style tools only',
    accent: 'accent',
    bestFor:
      'You only care about Kubernetes edges (type=kubernetes). No SSH, no Linux servers.',
    exposes: [
      'kube_get / kube_list / kube_describe (read)',
      'kube_apply / kube_delete / kube_exec when readOnly=false',
      'Routes through the kube API proxy of every connected kubernetes edge',
    ],
    pickWhen:
      "You manage clusters and want to scope what the LLM can touch — e.g. give the model a read-only Kubernetes MCP and skip your Linux fleet entirely.",
    pathHint: '/services/mcp/...',
  },
  {
    id: 'linux',
    icon: Terminal,
    title: 'Linux MCP',
    tagline: 'SSH-style tools only',
    accent: 'warning',
    bestFor:
      'You only care about server-type edges reached over SSH. No Kubernetes API access.',
    exposes: [
      'run_command / read_file / list_dir / stat_path (read)',
      'write_file / systemctl lifecycle / pkg install when readOnly=false',
      'Each tool takes an optional target= argument to pick a specific edge',
    ],
    pickWhen:
      "You're driving bare-metal or VM fleets and don't want the model to see your kube clusters at all.",
    pathHint: '/services/linux-mcp/...',
  },
]

// Each accent maps to a fixed bundle of Tailwind classes. Returning these
// from a function means Tailwind's content scanner can still see every
// literal class (string ternaries are visible to the JIT scanner), but the
// JS object hides the noise from the template.
const accentClass = (a: KindCard['accent']) => ({
  border:
    a === 'success'
      ? 'border-success/30'
      : a === 'warning'
      ? 'border-warning/30'
      : 'border-accent/30',
  bg:
    a === 'success'
      ? 'bg-success/5'
      : a === 'warning'
      ? 'bg-warning/5'
      : 'bg-accent/5',
  text:
    a === 'success'
      ? 'text-success'
      : a === 'warning'
      ? 'text-warning'
      : 'text-accent',
  dot:
    a === 'success'
      ? 'bg-success'
      : a === 'warning'
      ? 'bg-warning'
      : 'bg-accent',
  chip:
    a === 'success'
      ? 'border-success/30 bg-success/10 text-success'
      : a === 'warning'
      ? 'border-warning/30 bg-warning/10 text-warning'
      : 'border-accent/30 bg-accent/10 text-accent',
})
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm"
      @click.self="$emit('close')"
    >
      <div class="w-full max-w-3xl max-h-[90vh] overflow-y-auto rounded-2xl border border-border-subtle bg-surface-raised shadow-2xl">
        <!-- Header (matches CliQuickstartModal style) -->
        <div class="flex items-center justify-between border-b border-border-subtle bg-surface-overlay/60 px-4 py-2.5">
          <div class="flex items-center gap-2">
            <div class="flex items-center gap-1.5">
              <div class="h-2.5 w-2.5 rounded-full bg-danger/60" />
              <div class="h-2.5 w-2.5 rounded-full bg-warning/60" />
              <div class="h-2.5 w-2.5 rounded-full bg-success/60" />
            </div>
            <HelpCircle class="ml-2 h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            <span class="font-mono text-[11px] font-semibold tracking-wider text-text-secondary">
              kedge — MCP server kinds
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
            <h2 class="text-[15px] font-bold text-text-primary">Three flavours, one decision</h2>
            <p class="mt-1 text-[12px] text-text-muted">
              kedge exposes three Model Context Protocol endpoints. They all
              speak the same MCP wire format, but each one carves up
              <em>which</em> edges and <em>which</em> tools the AI can reach.
              Pick one based on what you want the model to see.
            </p>
          </div>

          <!-- Three cards -->
          <div class="space-y-3">
            <div
              v-for="card in cards"
              :key="card.id"
              class="rounded-xl border bg-surface-overlay/40 p-4"
              :class="[accentClass(card.accent).border, accentClass(card.accent).bg]"
            >
              <!-- Card header -->
              <div class="mb-3 flex items-center gap-2 flex-wrap">
                <component :is="card.icon" class="h-4 w-4" :class="accentClass(card.accent).text" :stroke-width="1.75" />
                <span class="text-[13px] font-bold text-text-primary">{{ card.title }}</span>
                <span
                  v-if="card.recommended"
                  class="rounded-full border px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider"
                  :class="accentClass(card.accent).chip"
                >
                  recommended
                </span>
                <span class="text-[11px] text-text-muted ml-auto">{{ card.tagline }}</span>
              </div>

              <p class="mb-3 text-[12px] text-text-secondary">
                {{ card.bestFor }}
              </p>

              <div class="mb-3">
                <span class="block text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                  What the AI sees
                </span>
                <ul class="mt-1.5 space-y-1">
                  <li
                    v-for="line in card.exposes"
                    :key="line"
                    class="flex gap-2 text-[12px] text-text-secondary"
                  >
                    <span class="mt-1.5 h-1 w-1 shrink-0 rounded-full" :class="accentClass(card.accent).dot" />
                    <span>{{ line }}</span>
                  </li>
                </ul>
              </div>

              <div class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-overlay/60 px-2.5 py-2">
                <Lightbulb class="mt-0.5 h-3 w-3 shrink-0" :class="accentClass(card.accent).text" :stroke-width="2" />
                <div class="min-w-0">
                  <span class="block text-[10px] font-semibold uppercase tracking-wider text-text-muted">Pick this when</span>
                  <span class="block text-[12px] text-text-secondary">{{ card.pickWhen }}</span>
                </div>
              </div>

              <div class="mt-3 flex items-center gap-2 text-[10px] text-text-muted">
                <span class="font-semibold uppercase tracking-wider">Path</span>
                <code class="rounded-md border border-border-subtle bg-surface-overlay px-1.5 py-0.5 font-mono">
                  {{ card.pathHint }}
                </code>
              </div>
            </div>
          </div>

          <!-- Footnote: the default global resources -->
          <div class="rounded-xl border border-border-subtle bg-surface-overlay/40 px-3 py-2.5">
            <span class="block text-[10px] font-semibold uppercase tracking-wider text-text-muted mb-1">
              Note
            </span>
            <p class="text-[11px] text-text-muted leading-relaxed">
              Each kind has a built-in
              <span class="font-mono text-text-secondary">default</span>
              instance that covers <em>every</em> matching edge — that's what
              the three cards on this page expose. Creating an additional MCP
              server lets you scope it to a labelled subset (e.g. only prod
              edges, only one region) and adjust read-only / toolset filters.
            </p>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
