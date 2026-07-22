<!--
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<!--
MCP Access page. MCP is a built-in, core-hosted provider: MCPServer is a named
CRD (distributed to tenant workspaces via the core.faros.sh APIExport), and the
in-core reconciler provisions each server's identity. A workspace can have many
servers — e.g. a read-only "audit" endpoint and a full-access "ops" one.

List view: a table of servers with create/delete. Detail view: per-server
connect snippets plus the live federated-provider tool inventory (stamped on
status.federatedProviders by the reconciler using that server's own identity).
-->

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { storeToRefs } from 'pinia'
import {
  Copy, Check, RefreshCw, Plug, Plus, Trash2, Boxes, ChevronRight, ChevronDown,
  CircleDot, Wrench, ArrowLeft,
} from 'lucide-vue-next'
import { authFetch } from '@/auth/session'
import { useTenantStore } from '@/stores/tenant'
import AppLayout from '@/components/AppLayout.vue'
import { confirmDialog } from '@/portalkit/confirm'
import ResourceTable from '@/components/ResourceTable.vue'

interface FederatedTool {
  name: string
  title?: string
  description?: string
}
interface FederatedProvider {
  name: string
  displayName?: string
  reachable: boolean
  message?: string
  tools?: FederatedTool[]
}
interface MCPServer {
  name: string
  displayName?: string
  instructions?: string
  readOnly?: boolean
  phase?: string
  url?: string
  federatedProviders?: FederatedProvider[]
  toolsRefreshedTime?: string
}
interface Connect {
  endpointURL: string
  serverName: string
  token: string
  tokenReady: boolean
}
type Client = 'claude-code' | 'claude-desktop' | 'codex'

const clients: { id: Client; label: string }[] = [
  { id: 'claude-code', label: 'Claude Code' },
  { id: 'claude-desktop', label: 'Claude Desktop' },
  { id: 'codex', label: 'Codex' },
]

const columns = [
  { key: 'name', label: 'Name' },
  { key: 'displayName', label: 'Display' },
  { key: 'phase', label: 'Status' },
  { key: 'tools', label: 'Tools' },
  { key: 'updated', label: 'Updated' },
  { key: 'actions', label: '' },
]

const tenant = useTenantStore()
const { orgUUID, workspaceUUID } = storeToRefs(tenant)

const loading = ref(true)
const error = ref<string | null>(null)
const servers = ref<MCPServer[]>([])

// Detail view selection (server name). Null = list view.
const selected = ref<string | null>(null)
const selectedServer = computed(() => servers.value.find((s) => s.name === selected.value) ?? null)

const rows = computed(() =>
  servers.value.map((s) => ({
    name: s.name,
    displayName: s.displayName || '—',
    phase: s.phase || 'Provisioning',
    tools: toolCount(s),
    updated: rel(s.toolsRefreshedTime),
    readOnly: s.readOnly,
    _server: s,
  })),
)

function toolCount(s: MCPServer): number {
  return (s.federatedProviders ?? []).reduce((n, p) => n + (p.tools?.length ?? 0), 0)
}

// Per-provider tool expand state within the detail view (keyed by provider name).
const openProviders = ref<Set<string>>(new Set())
function isProviderOpen(name: string): boolean {
  return openProviders.value.has(name)
}
function toggleProvider(name: string) {
  const next = new Set(openProviders.value)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  openProviders.value = next
}

const connect = ref<Record<string, Connect>>({})
const selectedClient = ref<Client>('claude-code')

const showCreate = ref(false)
const draft = ref({ name: '', displayName: '', instructions: '', readOnly: false })
const busy = ref(false)

const copiedField = ref<string | null>(null)
async function copy(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copiedField.value = field
    setTimeout(() => (copiedField.value = null), 2000)
  } catch {
    /* non-fatal */
  }
}

function base(): string | null {
  if (!orgUUID.value || !workspaceUUID.value) return null
  return `/api/orgs/${encodeURIComponent(orgUUID.value)}/workspaces/${encodeURIComponent(workspaceUUID.value)}/mcpservers`
}

async function load() {
  const b = base()
  if (!b) {
    loading.value = false
    error.value = 'Select an organization and workspace to manage MCP servers.'
    return
  }
  loading.value = true
  error.value = null
  try {
    const res = await authFetch(b, { tenant: true })
    if (!res.ok) throw new Error(`Failed to load MCP servers (${res.status})`)
    const body = (await res.json()) as { items?: MCPServer[] }
    servers.value = body.items ?? []
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch([orgUUID, workspaceUUID], load)

function openDetail(row: Record<string, unknown>) {
  selected.value = (row._server as MCPServer).name
  openProviders.value = new Set()
  loadConnect(selected.value)
}
function closeDetail() {
  selected.value = null
  load()
}

async function loadConnect(name: string) {
  const b = base()
  if (!b) return
  try {
    const res = await authFetch(`${b}/${encodeURIComponent(name)}/connect`, { tenant: true })
    if (!res.ok) throw new Error(`connect: ${res.status}`)
    connect.value = { ...connect.value, [name]: (await res.json()) as Connect }
  } catch {
    /* leave undefined; the detail shows a provisioning state */
  }
}

async function create() {
  const b = base()
  if (!b || !draft.value.name.trim()) return
  busy.value = true
  try {
    const res = await authFetch(b, {
      tenant: true,
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(draft.value),
    })
    if (!res.ok) throw new Error(`Create failed (${res.status})`)
    showCreate.value = false
    draft.value = { name: '', displayName: '', instructions: '', readOnly: false }
    await load()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

async function remove(name: string) {
  const b = base()
  if (!b) return
  if (!(await confirmDialog({ title: `Delete MCP server "${name}"?`, message: 'Its access token will be revoked.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    const res = await authFetch(`${b}/${encodeURIComponent(name)}`, { tenant: true, method: 'DELETE' })
    if (!res.ok && res.status !== 204) throw new Error(`Delete failed (${res.status})`)
    if (selected.value === name) selected.value = null
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

// ---- connect snippets (token masked on screen, injected on copy) ----
const TOKEN_PLACEHOLDER = '<token>'
const codexTokenEnvVar = 'KEDGE_MCP_TOKEN'
function shellQuote(v: string) {
  return `'${v.replace(/'/g, `'\\''`)}'`
}
function snippet(c: Connect, client: Client, token: string): string {
  if (client === 'claude-desktop') {
    return JSON.stringify({ mcpServers: { [c.serverName]: { url: c.endpointURL, headers: { Authorization: `Bearer ${token}` } } } }, null, 2)
  }
  if (client === 'codex') {
    return `export ${codexTokenEnvVar}=${shellQuote(token)}
codex mcp add ${c.serverName} \\
  --url ${shellQuote(c.endpointURL)} \\
  --bearer-token-env-var ${codexTokenEnvVar}`
  }
  return `claude mcp add --transport http ${c.serverName} ${shellQuote(c.endpointURL)} \\
  -H ${shellQuote(`Authorization: Bearer ${token}`)}`
}
const displaySnippet = computed(() => {
  const c = selected.value ? connect.value[selected.value] : null
  return c ? snippet(c, selectedClient.value, TOKEN_PLACEHOLDER) : ''
})
async function copySnippet() {
  const c = selected.value ? connect.value[selected.value] : null
  if (!c || !c.token) return
  await copy(snippet(c, selectedClient.value, c.token), 'snippet')
}

function phaseClass(p?: string): string {
  if (p === 'Ready') return 'text-success'
  if (p === 'Error') return 'text-danger'
  return 'text-text-muted'
}

function rel(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts).getTime()
  if (Number.isNaN(d)) return '—'
  const secs = Math.max(0, Math.floor((Date.now() - d) / 1000))
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}
</script>

<template>
  <AppLayout>
    <div>
      <!-- ─── LIST VIEW ─────────────────────────────────────────────── -->
      <template v-if="!selected">
        <div class="mb-6 flex items-center justify-between">
          <div class="flex items-center gap-3">
            <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-accent/10 text-accent">
              <Plug class="h-4.5 w-4.5" :stroke-width="1.75" />
            </div>
            <div>
              <h1 class="text-[17px] font-bold text-text-primary">MCP Access</h1>
              <p class="text-[12px] text-text-muted">Named endpoints that connect AI clients to this workspace's tools.</p>
            </div>
          </div>
          <button
            class="flex items-center gap-1.5 rounded-lg bg-accent px-3 py-2 text-[12px] font-medium text-white transition-all hover:opacity-90"
            @click="showCreate = !showCreate"
          >
            <Plus class="h-3.5 w-3.5" :stroke-width="2" /> New server
          </button>
        </div>

        <!-- Create form -->
        <section v-if="showCreate" class="mb-5 rounded-xl border border-border-subtle bg-surface-raised p-4">
          <div class="grid gap-3">
            <label class="grid gap-1">
              <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Name</span>
              <input v-model="draft.name" placeholder="ops" class="rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-primary" />
            </label>
            <label class="grid gap-1">
              <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Display name</span>
              <input v-model="draft.displayName" placeholder="Ops endpoint" class="rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[12px] text-text-primary" />
            </label>
            <label class="grid gap-1">
              <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Instructions (optional)</span>
              <textarea v-model="draft.instructions" rows="2" placeholder="This is production — ask before destructive operations." class="rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[12px] text-text-primary" />
            </label>
            <label class="flex items-center gap-2 text-[12px] text-text-secondary">
              <input v-model="draft.readOnly" type="checkbox" class="h-3.5 w-3.5" /> Read-only
            </label>
            <div class="flex justify-end gap-2">
              <button class="rounded-lg border border-border-subtle px-3 py-2 text-[12px] text-text-secondary hover:bg-surface-hover" @click="showCreate = false">Cancel</button>
              <button class="rounded-lg bg-accent px-3 py-2 text-[12px] font-medium text-white hover:opacity-90 disabled:opacity-40" :disabled="busy || !draft.name.trim()" @click="create">Create</button>
            </div>
          </div>
        </section>

        <ResourceTable
          :columns="columns"
          :rows="rows"
          :loading="loading"
          :error="error"
          empty-text="No MCP servers yet. Create one to connect an AI client."
          @row-click="openDetail"
        >
          <template #name="{ row }">
            <span class="font-mono font-semibold text-text-primary">{{ (row as any).name }}</span>
          </template>
          <template #displayName="{ value }">
            <span class="text-text-muted">{{ value }}</span>
          </template>
          <template #phase="{ value, row }">
            <span class="inline-flex items-center gap-1.5 text-[12px]" :class="phaseClass(value as string)">
              <CircleDot class="h-3 w-3" :stroke-width="2" /> {{ value }}
            </span>
            <span v-if="(row as any)?.readOnly" class="ml-2 rounded bg-surface-overlay px-1.5 py-0.5 text-[10px] text-text-muted">read-only</span>
          </template>
          <template #tools="{ value }">
            <span class="inline-flex items-center gap-1.5 text-text-muted">
              <Wrench class="h-3 w-3" :stroke-width="1.75" /> {{ value }}
            </span>
          </template>
          <template #updated="{ value }">
            <span class="text-text-muted">{{ value }}</span>
          </template>
          <template #actions="{ row }">
            <div class="flex justify-end">
              <button
                class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-danger/10 hover:text-danger"
                title="Delete"
                @click.stop="remove((row as any).name)"
              >
                <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
              </button>
            </div>
          </template>
        </ResourceTable>
      </template>

      <!-- ─── DETAIL VIEW ───────────────────────────────────────────── -->
      <template v-else-if="selectedServer">
        <div class="mb-6 flex items-start justify-between">
          <div class="flex items-center gap-3">
            <button class="flex h-8 w-8 items-center justify-center rounded-lg text-text-muted hover:bg-surface-hover hover:text-text-primary" title="Back" @click="closeDetail">
              <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
            </button>
            <div>
              <div class="flex items-center gap-2">
                <h1 class="font-mono text-[16px] font-bold text-text-primary">{{ selectedServer.name }}</h1>
                <span class="inline-flex items-center gap-1 text-[12px]" :class="phaseClass(selectedServer.phase)">
                  <CircleDot class="h-3 w-3" :stroke-width="2" /> {{ selectedServer.phase || 'Provisioning' }}
                </span>
                <span v-if="selectedServer.readOnly" class="rounded bg-surface-overlay px-1.5 py-0.5 text-[10px] text-text-muted">read-only</span>
              </div>
              <p class="text-[12px] text-text-muted">{{ selectedServer.displayName || 'MCP endpoint' }}</p>
            </div>
          </div>
          <button class="flex items-center gap-1.5 rounded-lg border border-border-subtle px-2.5 py-1.5 text-[12px] text-text-secondary hover:border-danger/40 hover:text-danger" @click="remove(selectedServer.name)">
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" /> Delete
          </button>
        </div>

        <!-- Connect -->
        <section class="mb-6 rounded-xl border border-border-subtle bg-surface-raised p-4">
          <h2 class="mb-3 text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Connect an AI client</h2>
          <template v-if="connect[selectedServer.name]">
            <div class="mb-3 flex items-center gap-2">
              <code class="min-w-0 flex-1 truncate rounded-lg bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-secondary">{{ connect[selectedServer.name].endpointURL }}</code>
              <button class="flex h-8 items-center gap-1.5 rounded-lg border border-border-subtle px-2.5 text-[12px] text-text-secondary hover:bg-surface-hover" @click="copy(connect[selectedServer.name].endpointURL, 'url')">
                <Check v-if="copiedField === 'url'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" /> Copy
              </button>
            </div>
            <div v-if="!connect[selectedServer.name].tokenReady" class="mb-3 flex items-center justify-between rounded-lg border border-warning/30 bg-warning/5 p-3 text-[12px] text-warning">
              <span>Token is still being provisioned.</span>
              <button class="flex items-center gap-1.5 rounded-lg border border-warning/30 px-2 py-1 hover:bg-warning/10" @click="loadConnect(selectedServer.name)">
                <RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" /> Refresh
              </button>
            </div>
            <div class="mb-2 flex gap-1.5">
              <button
                v-for="c in clients"
                :key="c.id"
                class="rounded-lg border px-2.5 py-1.5 text-[12px] transition-all"
                :class="selectedClient === c.id ? 'border-accent bg-accent/10 text-accent' : 'border-border-subtle text-text-secondary hover:bg-surface-hover'"
                @click="selectedClient = c.id"
              >
                {{ c.label }}
              </button>
            </div>
            <div class="relative">
              <pre class="overflow-x-auto rounded-lg bg-surface-overlay p-3 font-mono text-[12px] leading-relaxed text-text-secondary"><code>{{ displaySnippet }}</code></pre>
              <button
                class="absolute right-2 top-2 flex h-7 items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-raised px-2.5 text-[11px] text-text-secondary hover:bg-surface-hover disabled:opacity-40"
                :disabled="!connect[selectedServer.name].tokenReady"
                @click="copySnippet"
              >
                <Check v-if="copiedField === 'snippet'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" /> Copy
              </button>
            </div>
            <p class="mt-2 text-[11px] text-text-muted">Token is masked and injected only on copy. Keep it secret.</p>
          </template>
          <div v-else class="text-[12px] text-text-muted">Loading connect details…</div>
        </section>

        <!-- Federated providers → tools -->
        <section>
          <div class="mb-3 flex items-center gap-2">
            <Boxes class="h-4 w-4 text-text-muted" :stroke-width="1.75" />
            <h2 class="text-[13px] font-semibold text-text-primary">Providers &amp; tools</h2>
            <span class="text-[11px] text-text-muted">what this endpoint federates, discovered with its own identity</span>
            <span v-if="selectedServer.toolsRefreshedTime" class="ml-auto text-[11px] text-text-muted">updated {{ rel(selectedServer.toolsRefreshedTime) }}</span>
          </div>

          <div v-if="!selectedServer.federatedProviders?.length" class="rounded-xl border border-border-subtle bg-surface-raised p-6 text-center text-[13px] text-text-muted">
            No providers are federating tools into this endpoint yet. Enable a provider (infrastructure, code, edges…) or wait for the next refresh.
          </div>
          <div v-else class="grid gap-2">
            <div v-for="p in selectedServer.federatedProviders" :key="p.name" class="rounded-xl border border-border-subtle bg-surface-raised">
              <button class="flex w-full items-center justify-between p-3.5 text-left" @click="toggleProvider(p.name)">
                <div class="flex min-w-0 items-center gap-2">
                  <component :is="isProviderOpen(p.name) ? ChevronDown : ChevronRight" class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="2" />
                  <span class="font-mono text-[13px] font-semibold text-text-primary">{{ p.displayName || p.name }}</span>
                  <span class="inline-flex items-center gap-1 text-[11px]" :class="p.reachable ? 'text-success' : 'text-danger'">
                    <CircleDot class="h-3 w-3" :stroke-width="2" /> {{ p.reachable ? 'Reachable' : 'Unreachable' }}
                  </span>
                </div>
                <span class="flex shrink-0 items-center gap-1.5 text-[11px] text-text-muted">
                  <Wrench class="h-3 w-3" :stroke-width="1.75" /> {{ p.tools?.length ?? 0 }} {{ (p.tools?.length ?? 0) === 1 ? 'tool' : 'tools' }}
                </span>
              </button>
              <div v-if="isProviderOpen(p.name)" class="border-t border-border-subtle p-3.5">
                <div v-if="p.message" class="mb-2 rounded-lg border border-danger/30 bg-danger/5 p-2.5 text-[12px] text-danger">{{ p.message }}</div>
                <div v-if="!p.tools?.length && !p.message" class="text-[12px] text-text-muted">This provider advertises no tools right now.</div>
                <ul v-else class="grid gap-1.5">
                  <li v-for="t in p.tools" :key="t.name" class="rounded-lg bg-surface-overlay px-3 py-2">
                    <div class="flex items-baseline gap-2">
                      <code class="font-mono text-[12px] font-semibold text-text-primary">{{ t.name }}</code>
                      <span v-if="t.title && t.title !== t.name" class="text-[11px] text-text-muted">{{ t.title }}</span>
                    </div>
                    <p v-if="t.description" class="mt-0.5 text-[11px] leading-relaxed text-text-secondary">{{ t.description }}</p>
                  </li>
                </ul>
              </div>
            </div>
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>
