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
import { useRoute, useRouter } from 'vue-router'
import {
  Copy, Check, RefreshCw, Plug, Plus, ChevronRight, ChevronDown,
  Wrench, ArrowLeft, Ellipsis, ShieldCheck,
} from 'lucide-vue-next'
import { authFetch } from '@/auth/session'
import { useTenantStore } from '@/stores/tenant'
import AppLayout from '@/components/AppLayout.vue'
import { confirmDialog } from '@/portalkit/confirm'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import ResourcePage from '@/portalkit/ResourcePage.vue'
import ResourceSectionCard from '@/portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '@/portalkit/ResourceStatCards.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'

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
const route = useRoute()
const router = useRouter()

const loading = ref(true)
const loaded = ref(false)
const error = ref<string | null>(null)
const servers = ref<MCPServer[]>([])

// Detail view selection is route-owned so a server can be opened directly or
// shared as /ui/mcp/:name. Null means the collection view.
const selected = computed<string | null>(() => {
  const name = route.params.name
  return typeof name === 'string' && name.length > 0 ? name : null
})
const selectedServer = computed(() => servers.value.find((s) => s.name === selected.value) ?? null)
const selectedResourceMissing = computed(() => Boolean(selected.value && loaded.value && !selectedServer.value))

const readState = computed<boolean | null>(() => {
  if (selectedResourceMissing.value) return false
  if (loaded.value && selectedServer.value) return true
  if (error.value) return false
  return loading.value ? false : null
})
const readError = computed(() => selectedResourceMissing.value
  ? `MCP server "${selected.value}" was not found in this workspace.`
  : error.value)

const rows = computed(() =>
  servers.value.map((s) => ({
    name: s.name,
    displayName: s.displayName || '—',
    phase: pendingDeletion.value === s.name ? 'Deleting' : s.phase || 'Provisioning',
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
const connectLoading = ref(false)
const connectError = ref<string | null>(null)
const pendingDeletion = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const refreshMode = ref<'foreground' | 'background'>('foreground')
let listRequestID = 0
let connectRequestID = 0
const actionsMenu = ref<HTMLDetailsElement | null>(null)

const selectedConnect = computed(() => selected.value ? connect.value[selected.value] ?? null : null)
const selectedPhase = computed(() => {
  if (selectedResourceMissing.value) return 'Unavailable'
  if (!selectedServer.value && loading.value) return 'Loading'
  if (selected.value && pendingDeletion.value === selected.value) return 'Deleting'
  return selectedServer.value?.phase || 'Provisioning'
})
const selectedStatusTone = computed<'success' | 'warning' | 'danger' | 'muted' | null>(() => {
  if (selectedPhase.value === 'Deleting' || selectedPhase.value === 'Provisioning' || selectedPhase.value === 'Loading') return 'warning'
  if (selectedPhase.value === 'Error' || selectedPhase.value === 'Unavailable') return 'danger'
  if (selectedPhase.value === 'Ready') return 'success'
  // Leave other controller phases to StatusBadge's shared vocabulary (for
  // example Pending, Running, and Terminating) instead of flattening them to
  // a misleading neutral tone.
  return null
})
const deleting = computed(() => Boolean(selected.value && pendingDeletion.value === selected.value))
const foregroundRefreshing = computed(() => loading.value && refreshMode.value === 'foreground')

const statCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'access',
    label: 'Access',
    value: selectedServer.value?.readOnly ? 'Read-only' : 'Full access',
    icon: ShieldCheck,
  },
  {
    id: 'providers',
    label: 'Providers',
    value: selectedServer.value?.federatedProviders?.length ?? 0,
    detail: 'Federated sources',
    icon: Plug,
  },
  {
    id: 'tools',
    label: 'Tools',
    value: selectedServer.value ? toolCount(selectedServer.value) : 0,
    detail: 'Discovered tools',
    icon: Wrench,
  },
])

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

function tenantIdentity(name?: string | null): string {
  return `${orgUUID.value ?? ''}/${workspaceUUID.value ?? ''}/${name ?? ''}`
}

async function load() {
  const b = base()
  if (!b) {
    loaded.value = false
    loading.value = false
    error.value = 'Select an organization and workspace to manage MCP servers.'
    return
  }
  const requestID = ++listRequestID
  const identity = tenantIdentity()
  refreshMode.value = 'foreground'
  loading.value = true
  error.value = null
  try {
    const res = await authFetch(b, { tenant: true })
    if (!res.ok) throw new Error(`Failed to load MCP servers (${res.status})`)
    const body = (await res.json()) as { items?: MCPServer[] }
    if (requestID !== listRequestID || identity !== tenantIdentity()) return
    servers.value = body.items ?? []
    loaded.value = true
    if (pendingDeletion.value && !servers.value.some((server) => server.name === pendingDeletion.value)) {
      const deletedName = pendingDeletion.value
      pendingDeletion.value = null
      if (selected.value === deletedName) void router.replace({ name: 'mcp' })
    }
    if (selected.value && selectedServer.value) void loadConnect(selected.value)
  } catch (e) {
    if (requestID === listRequestID && identity === tenantIdentity()) error.value = (e as Error).message
  } finally {
    if (requestID === listRequestID && identity === tenantIdentity()) loading.value = false
  }
}

onMounted(load)
watch([orgUUID, workspaceUUID], () => {
  // Connect responses contain a bearer token. Never allow a response or
  // cached token from the previous tenant to survive a context switch.
  listRequestID += 1
  connectRequestID += 1
  connect.value = {}
  connectError.value = null
  // The list is a tenant-owned snapshot too. Drop it before the replacement
  // read so a same-named server from the previous workspace is never shown.
  servers.value = []
  loaded.value = false
  loading.value = true
  error.value = null
  pendingDeletion.value = null
  mutationError.value = null
  openProviders.value = new Set()
  void load()
})

function openDetail(row: Record<string, unknown>) {
  const name = (row._server as MCPServer).name
  void router.push({ name: 'mcp-detail', params: { name } })
}
function closeDetail() {
  void router.push({ name: 'mcp' })
}

function deleteFromMenu() {
  actionsMenu.value?.removeAttribute('open')
  if (selected.value) void remove(selected.value)
}

async function loadConnect(name: string) {
  const b = base()
  if (!b) {
    if (selected.value === name) connectError.value = 'Select an organization and workspace before loading connect details.'
    return
  }
  const requestID = ++connectRequestID
  const identity = tenantIdentity(name)
  connectLoading.value = true
  if (selected.value === name) connectError.value = null
  try {
    const res = await authFetch(`${b}/${encodeURIComponent(name)}/connect`, { tenant: true })
    if (!res.ok) throw new Error(`connect: ${res.status}`)
    const next = (await res.json()) as Connect
    if (requestID !== connectRequestID || identity !== tenantIdentity(name) || selected.value !== name) return
    connect.value = { ...connect.value, [name]: next }
    connectError.value = null
  } catch (e) {
    if (requestID === connectRequestID && identity === tenantIdentity(name) && selected.value === name) {
      connectError.value = (e as Error).message
    }
  } finally {
    if (requestID === connectRequestID) connectLoading.value = false
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
  const identity = tenantIdentity(name)
  const routeIdentity = selected.value
  const expectedServer = servers.value.find((server) => server.name === name) ?? null
  if (!(await confirmDialog({ title: `Delete MCP server "${name}"?`, message: 'Its access token will be revoked.', danger: true, confirmLabel: 'Delete' }))) return

  // Confirmation is asynchronous. Re-check both the tenant and the route (and
  // the object snapshot when one exists) before sending a request so a delayed
  // confirmation cannot delete or report on a different server.
  const sameDeleteContext = () =>
    identity === tenantIdentity(name) &&
    selected.value === routeIdentity &&
    (routeIdentity === null || selected.value === name)
  const sameDeleteTarget = () =>
    sameDeleteContext() &&
    (!expectedServer || servers.value.find((server) => server.name === name) === expectedServer)
  if (!sameDeleteTarget()) return

  const b = base()
  if (!b) return
  pendingDeletion.value = name
  mutationError.value = null
  try {
    const res = await authFetch(`${b}/${encodeURIComponent(name)}`, { tenant: true, method: 'DELETE' })
    if (!res.ok && res.status !== 204) throw new Error(`Delete failed (${res.status})`)
    if (!sameDeleteContext()) return
    // A successful DELETE is authoritative for this object. Remove the local
    // row and clear its credential before navigating, so recovery never
    // depends on a second list request succeeding.
    servers.value = servers.value.filter((server) => server.name !== name)
    pendingDeletion.value = null
    connect.value = {}
    connectRequestID += 1
    if (selected.value === name) void router.replace({ name: 'mcp' })
    // Reconcile the collection in the background. A transient list failure
    // is surfaced as stale collection state, but cannot strand the detail
    // route or leave it disabled in a deletion-pending state.
    void load()
  } catch (e) {
    if (
      sameDeleteContext() &&
      // Keep list-view errors collection-scoped while requiring an exact
      // detail route match when the user is viewing a server.
      (selected.value === name || selected.value === routeIdentity)
    ) {
      pendingDeletion.value = null
      mutationError.value = (e as Error).message
    }
  }
}

watch(selected, (name, previousName) => {
  if (name === previousName) return
  // A server name is part of the credential identity. Clear the previous
  // connect response before rendering the new detail route.
  connectRequestID += 1
  connect.value = {}
  connectError.value = null
  pendingDeletion.value = null
  mutationError.value = null
  openProviders.value = new Set()
  selectedClient.value = 'claude-code'
  if (name && loaded.value) void loadConnect(name)
}, { immediate: true })

// ---- connect snippets (token masked on screen, injected on copy) ----
const TOKEN_PLACEHOLDER = '<token>'
const codexTokenEnvVar = 'FAROS_MCP_TOKEN'
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
  const c = selectedConnect.value
  return c ? snippet(c, selectedClient.value, TOKEN_PLACEHOLDER) : ''
})
async function copySnippet() {
  const c = selectedConnect.value
  if (!c || !c.token) return
  await copy(snippet(c, selectedClient.value, c.token), 'snippet')
}

function providerPanelID(name: string, index: number): string {
  const slug = name.toLowerCase().replace(/[^a-z0-9_-]+/g, '-')
  return `mcp-provider-${index}-${slug || 'provider'}`
}

function clientPanelID(client: Client): string {
  return `mcp-client-${client}-snippet`
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
            type="button"
            class="k-btn k-btn--primary px-3 py-2 text-[12px]"
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
              <input v-model="draft.name" placeholder="ops" class="k-input font-mono text-[12px]" />
            </label>
            <label class="grid gap-1">
              <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Display name</span>
              <input v-model="draft.displayName" placeholder="Ops endpoint" class="k-input text-[12px]" />
            </label>
            <label class="grid gap-1">
              <span class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Instructions (optional)</span>
              <textarea v-model="draft.instructions" rows="2" placeholder="This is production — ask before destructive operations." class="k-input text-[12px]" />
            </label>
            <label class="flex items-center gap-2 text-[12px] text-text-secondary">
              <input v-model="draft.readOnly" type="checkbox" class="k-checkbox" /> Read-only
            </label>
            <div class="flex justify-end gap-2">
              <button type="button" class="k-btn k-btn--ghost px-3 py-2 text-[12px]" @click="showCreate = false">Cancel</button>
              <button type="button" class="k-btn k-btn--primary px-3 py-2 text-[12px]" :disabled="busy || !draft.name.trim()" @click="create">Create</button>
            </div>
          </div>
        </section>

        <div v-if="mutationError" class="mb-5 flex items-center gap-3 rounded-md border border-danger/30 bg-danger/5 p-3 text-[12px] text-danger" role="alert" aria-live="assertive">
          <span class="min-w-0 flex-1">{{ mutationError }}</span>
          <button type="button" class="k-btn k-btn--ghost shrink-0 px-2 py-1 text-danger" @click="mutationError = null">Dismiss</button>
        </div>

        <ResourceTable
          :columns="columns"
          :rows="rows"
          searchable
          search-placeholder="Search MCP servers…"
          :filters="[{ key: 'phase', label: 'Status', allLabel: 'Any status' }]"
          paginated
          :page-size="10"
          row-key="name"
          :loaded="loaded"
          :loading="loading"
          :error="error"
          :stale="loaded && !!error"
          retryable
          empty-text="No MCP servers yet. Create one to connect an AI client."
          :row-aria-label="(row) => `Open MCP server ${String(row.name)}`"
          @retry="load"
          @row-click="openDetail"
        >
          <template #name="{ row }">
            <span class="font-mono font-semibold text-text-primary">{{ (row as any).name }}</span>
          </template>
          <template #displayName="{ value }">
            <span class="text-text-muted">{{ value }}</span>
          </template>
          <template #phase="{ value, row }">
            <StatusBadge :status="String(value)" />
            <span v-if="(row as any)?.readOnly" class="ml-2 rounded bg-surface-overlay px-1.5 py-0.5 text-[10px] text-text-muted">read-only</span>
          </template>
          <template #tools="{ value }">
            <span class="inline-flex items-center gap-1.5 text-text-muted">
              <Wrench class="h-3 w-3" :stroke-width="2" /> {{ value }}
            </span>
          </template>
          <template #updated="{ value }">
            <span class="text-text-muted">{{ value }}</span>
          </template>
          <template #actions="{ row }">
            <div class="flex justify-end">
              <ResourceTableDeleteButton
                :label="`Delete MCP server ${String((row as any).name)}`"
                :busy="pendingDeletion === (row as any).name"
                @click="remove((row as any).name)"
              />
            </div>
          </template>
        </ResourceTable>
      </template>

      <!-- ─── DETAIL VIEW ───────────────────────────────────────────── -->
      <template v-else>
        <a
          class="k-btn k-btn--ghost k-back-action"
          href="/ui/mcp"
          @click.prevent="closeDetail"
        >
          <ArrowLeft :size="14" aria-hidden="true" />
          MCP Access
        </a>

        <ResourcePage
          :title="selectedServer?.name || selected || 'MCP server'"
          kind="MCP server"
          :subtitle="selectedServer?.displayName || 'Named endpoint for workspace tools'"
          :loaded="readState"
          :loading="loading"
          :refresh-mode="refreshMode"
          :error="readError"
          :stale="loaded && !!error"
          retryable
          @retry="load"
        >
          <template #meta>
            <span v-if="selectedServer">MCP Access</span>
            <span v-else>Resource snapshot unavailable</span>
            <span aria-hidden="true">·</span>
            <span v-if="selectedServer?.readOnly">read-only access</span>
            <span v-else-if="selectedServer">full access</span>
            <span v-else>retry to load this server</span>
          </template>

          <template #status>
            <StatusBadge :status="selectedPhase" :tone="selectedStatusTone" />
          </template>

          <template #actions>
            <div class="flex items-center gap-2" role="group" aria-label="MCP server actions">
              <button
                type="button"
                class="k-btn k-btn--ghost"
                :disabled="foregroundRefreshing || deleting || !selectedServer"
                :aria-busy="foregroundRefreshing || undefined"
                @click="load"
              >
                <RefreshCw :size="14" :class="{ 'animate-spin': foregroundRefreshing }" aria-hidden="true" />
                {{ foregroundRefreshing ? 'Refreshing…' : 'Refresh' }}
              </button>
              <details ref="actionsMenu" class="relative">
                <summary class="k-btn k-btn--ghost list-none" aria-label="More MCP server actions">
                  <Ellipsis :size="16" aria-hidden="true" />
                  <span class="sr-only">More actions</span>
                </summary>
                <div class="absolute right-0 top-full z-20 mt-1 min-w-40 rounded-md border border-border-default bg-surface-overlay p-1 shadow-lg">
                  <button
                    type="button"
                    class="block w-full rounded-sm px-2.5 py-2 text-left text-[12px] text-danger hover:bg-danger-subtle disabled:cursor-not-allowed disabled:opacity-40"
                    :disabled="!selectedServer || deleting || loading"
                    @click="deleteFromMenu"
                  >
                    {{ deleting ? 'Deleting server…' : 'Delete server' }}
                  </button>
                </div>
              </details>
            </div>
          </template>

          <template #summary>
            <ResourceStatCards :cards="statCards" density="compact" aria-label="MCP server summary" />
          </template>

          <template #body>
            <template v-if="selectedServer">
              <p v-if="deleting" class="rounded-md border border-warning/30 bg-warning/5 p-3 text-[12px] text-warning" role="status" aria-live="polite">
                Deleting this MCP server. The last successful snapshot remains visible until the hub confirms removal.
              </p>
              <p v-if="mutationError" class="rounded-md border border-danger/30 bg-danger/5 p-3 text-[12px] text-danger" role="alert" aria-live="assertive">
                {{ mutationError }}
              </p>

              <div class="grid min-w-0 gap-4">
                <ResourceSectionCard id="mcp-connect" eyebrow="Connection" title="Connect an AI client" description="Use a client-specific setup snippet to connect to this workspace endpoint.">
                  <template v-if="selectedConnect">
                    <div class="flex min-w-0 items-center gap-2">
                      <code class="min-w-0 flex-1 truncate rounded-md bg-surface-overlay px-3 py-2 font-mono text-[12px] text-text-secondary">{{ selectedConnect.endpointURL }}</code>
                      <button type="button" class="k-btn k-btn--ghost h-8 px-2.5 text-[12px]" @click="copy(selectedConnect.endpointURL, 'url')">
                        <Check v-if="copiedField === 'url'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                        <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" /> Copy endpoint
                      </button>
                    </div>
                    <div v-if="connectError" class="flex items-center justify-between gap-3 rounded-md border border-danger/30 bg-danger/5 p-3 text-[12px] text-danger" role="alert" aria-live="assertive">
                      <span>{{ connectError }}</span>
                      <button type="button" class="k-btn k-btn--ghost shrink-0 px-2 py-1 text-danger" :disabled="connectLoading" @click="loadConnect(selectedServer.name)">
                        <RefreshCw :size="13" :class="{ 'animate-spin': connectLoading }" aria-hidden="true" /> Retry
                      </button>
                    </div>
                    <div v-if="!selectedConnect.tokenReady" class="flex items-center justify-between gap-3 rounded-md border border-warning/30 bg-warning/5 p-3 text-[12px] text-warning" role="status" aria-live="polite">
                      <span>Token is still being provisioned.</span>
                      <button type="button" class="k-btn k-btn--ghost shrink-0 px-2 py-1 text-warning hover:border-warning/40 hover:bg-warning-subtle" :disabled="connectLoading" @click="loadConnect(selectedServer.name)">
                        <RefreshCw :size="13" :class="{ 'animate-spin': connectLoading }" aria-hidden="true" /> Refresh
                      </button>
                    </div>
                    <div v-if="connectLoading" class="text-[11px] text-text-muted" role="status" aria-live="polite">Updating connect details…</div>

                    <div class="grid min-w-0 gap-2">
                      <div class="flex gap-1.5" role="tablist" aria-label="AI client setup">
                        <button
                          v-for="c in clients"
                          :id="`${clientPanelID(c.id)}-tab`"
                          :key="c.id"
                          type="button"
                          role="tab"
                          :aria-selected="selectedClient === c.id"
                          :aria-controls="clientPanelID(c.id)"
                          class="k-btn k-btn--ghost px-2.5 py-1.5 text-[12px] transition-all"
                          :class="selectedClient === c.id ? 'border-accent bg-accent/10 text-accent' : 'border-border-subtle text-text-secondary hover:bg-surface-hover'"
                          @click="selectedClient = c.id"
                        >
                          {{ c.label }}
                        </button>
                      </div>
                      <div class="relative min-w-0 max-w-full" role="tabpanel" :id="clientPanelID(selectedClient)" :aria-labelledby="`${clientPanelID(selectedClient)}-tab`" tabindex="0">
                        <pre class="block w-full min-w-0 max-w-full overflow-x-auto whitespace-pre rounded-md bg-surface-overlay p-3 font-mono text-[12px] leading-relaxed text-text-secondary"><code>{{ displaySnippet }}</code></pre>
                        <button
                          type="button"
                          class="k-btn k-btn--ghost absolute right-2 top-2 h-7 px-2.5 text-[11px] disabled:opacity-40"
                          :disabled="!selectedConnect.tokenReady"
                          @click="copySnippet"
                        >
                          <Check v-if="copiedField === 'snippet'" class="h-3.5 w-3.5 text-success" :stroke-width="2" />
                          <Copy v-else class="h-3.5 w-3.5" :stroke-width="1.75" /> Copy
                        </button>
                      </div>
                    </div>
                    <p class="text-[11px] text-text-muted">Token is masked and injected only on copy. Keep it secret.</p>
                  </template>
                  <div v-else-if="connectError" class="flex items-center justify-between gap-3 rounded-md border border-danger/30 bg-danger/5 p-3 text-[12px] text-danger" role="alert" aria-live="assertive">
                    <span>{{ connectError }}</span>
                    <button type="button" class="k-btn k-btn--ghost shrink-0 px-2 py-1 text-danger" :disabled="connectLoading" @click="loadConnect(selectedServer.name)">
                      <RefreshCw :size="13" :class="{ 'animate-spin': connectLoading }" aria-hidden="true" /> Retry
                    </button>
                  </div>
                  <div v-else class="text-[12px] text-text-muted" role="status" aria-live="polite">Loading connect details…</div>
                </ResourceSectionCard>

                <ResourceSectionCard v-if="selectedServer.instructions" id="mcp-instructions" eyebrow="Guidance" title="Instructions" description="Instructions supplied for clients using this MCP endpoint.">
                  <p class="whitespace-pre-wrap text-[12px] leading-relaxed text-text-secondary">{{ selectedServer.instructions }}</p>
                </ResourceSectionCard>

                <ResourceSectionCard id="mcp-providers" eyebrow="Discovery" title="Providers &amp; tools" description="Tools federated into this endpoint, discovered with its own identity.">
                  <template #actions>
                    <span v-if="selectedServer.toolsRefreshedTime" class="text-[11px] text-text-muted">updated {{ rel(selectedServer.toolsRefreshedTime) }}</span>
                  </template>
                  <div v-if="!selectedServer.federatedProviders?.length" class="rounded-md border border-border-subtle bg-surface-overlay p-5 text-center text-[13px] text-text-muted">
                    No providers are federating tools into this endpoint yet. Enable a provider (infrastructure, code, edges…) or wait for the next refresh.
                  </div>
                  <div v-else class="grid gap-2">
                    <div v-for="(p, providerIndex) in selectedServer.federatedProviders" :key="p.name" class="overflow-hidden rounded-md border border-border-subtle bg-surface-overlay">
                      <button
                        type="button"
                        class="k-btn k-btn--ghost flex w-full items-center justify-between rounded-none border-0 bg-transparent p-3.5 text-left hover:bg-surface-hover"
                        :aria-expanded="isProviderOpen(p.name)"
                        :aria-controls="providerPanelID(p.name, providerIndex)"
                        @click="toggleProvider(p.name)"
                      >
                        <span class="flex min-w-0 items-center gap-2">
                          <component :is="isProviderOpen(p.name) ? ChevronDown : ChevronRight" class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="2" aria-hidden="true" />
                          <span class="truncate font-mono text-[13px] font-semibold text-text-primary">{{ p.displayName || p.name }}</span>
                          <StatusBadge :status="p.reachable ? 'Reachable' : 'Unreachable'" :tone="p.reachable ? 'success' : 'danger'" />
                        </span>
                        <span class="flex shrink-0 items-center gap-1.5 text-[11px] text-text-muted">
                          <Wrench class="h-3 w-3" :stroke-width="2" aria-hidden="true" /> {{ p.tools?.length ?? 0 }} {{ (p.tools?.length ?? 0) === 1 ? 'tool' : 'tools' }}
                        </span>
                      </button>
                      <div v-if="isProviderOpen(p.name)" :id="providerPanelID(p.name, providerIndex)" class="border-t border-border-subtle p-3.5" role="region" :aria-label="`${p.displayName || p.name} tools`">
                        <div v-if="p.message" class="mb-2 rounded-md border border-danger/30 bg-danger/5 p-2.5 text-[12px] text-danger">{{ p.message }}</div>
                        <div v-if="!p.tools?.length && !p.message" class="text-[12px] text-text-muted">This provider advertises no tools right now.</div>
                        <ul v-else class="grid gap-1.5">
                          <li v-for="t in p.tools" :key="t.name" class="rounded-md bg-surface p-3">
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
                </ResourceSectionCard>
              </div>
            </template>
          </template>
        </ResourcePage>
      </template>
    </div>
  </AppLayout>
</template>
