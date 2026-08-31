<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import SelfHostInstructions from '@/components/SelfHostInstructions.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { useProvidersStore, type ProviderDTO, type PermissionClaim } from '@/stores/providers'
import { useOrgProvidersStore, type OrgProviderRegistration } from '@/stores/orgProviders'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'
import { Puzzle, ExternalLink, AlertCircle, AlertTriangle, ArrowUpCircle, Plus, X, Loader2, Search, Server, Trash2, RefreshCw } from 'lucide-vue-next'

const providers = useProvidersStore()
const orgProviders = useOrgProvidersStore()

// Two views of the same catalog: what you can turn on ("Catalog"), and what you
// can run yourself ("Self-Hosting"). They are separate tabs rather than one list
// because the actions differ in kind — enabling binds an API in a workspace,
// self-hosting hands you a credential and a deployment to run.
type Tab = 'catalog' | 'self-hosting'
const tab = ref<Tab>('catalog')
const tabs: { key: Tab; label: string }[] = [
  { key: 'catalog', label: 'Catalog' },
  { key: 'self-hosting', label: 'Self-Hosting' },
]

// The provider whose install details are open, and what the hub returned for it.
const activeRegistration = ref<OrgProviderRegistration | null>(null)
const selfHostBusy = ref<Record<string, boolean>>({})
const selfHostError = ref<string | null>(null)

// Which cluster a new self-hosted provider goes into. Keyed "workspace/name"
// so it survives a reload of the target list by value rather than identity.
const selectedEdgeKey = ref<string>('')

const eligibleEdges = computed(() => orgProviders.eligibleInstallTargets)

// Default to the only eligible cluster, or the first one, so the common case
// (one cluster) needs no interaction. Recomputed rather than watched: the list
// is small and this keeps the selection valid if a cluster drops out.
const selectedEdge = computed(() => {
  const list = eligibleEdges.value
  if (!list.length) return null
  return list.find((t) => `${t.workspace}/${t.name}` === selectedEdgeKey.value) ?? list[0]
})

function edgeLabel(t: { workspace: string; workspaceDisplayName?: string; name: string }): string {
  return `${t.name} · ${t.workspaceDisplayName || t.workspace}`
}

// canSelfHost is the single gate. A provider cannot be installed into a cluster
// the hub cannot reach, and the hub refuses such a registration anyway — so the
// button is disabled rather than left to 409.
const canSelfHost = computed(() => orgProviders.installTargetsEligible && !!selectedEdge.value)

async function selfHost(p: ProviderDTO) {
  const edge = selectedEdge.value
  if (!edge) {
    selfHostError.value = orgProviders.installTargetsReason ?? 'no connected cluster to install into'
    return
  }
  selfHostError.value = null
  selfHostBusy.value = { ...selfHostBusy.value, [p.name]: true }
  try {
    // Same name as the platform provider: the chart registers its CatalogEntry
    // under its own name, and the org's copy shadows the platform one for this
    // org only.
    activeRegistration.value = await orgProviders.register(p.name, p.name, edge)
  } catch (e) {
    selfHostError.value = e instanceof Error ? e.message : String(e)
    // The hub is authoritative on eligibility and its answer may have changed
    // under us (an agent dropping between page load and click). Re-check so the
    // buttons reflect reality instead of failing the same way again.
    orgProviders.loadInstallTargets()
  } finally {
    selfHostBusy.value = { ...selfHostBusy.value, [p.name]: false }
  }
}

async function showInstructions(name: string) {
  selfHostError.value = null
  selfHostBusy.value = { ...selfHostBusy.value, [name]: true }
  try {
    activeRegistration.value = await orgProviders.instructions(name)
  } catch (e) {
    selfHostError.value = e instanceof Error ? e.message : String(e)
  } finally {
    selfHostBusy.value = { ...selfHostBusy.value, [name]: false }
  }
}

async function removeSelfHosted(name: string) {
  if (!(await confirmDialog({
    title: `Remove self-hosted "${name}"?`,
    message: 'This deletes its workspace and everything in it, including its API. Workspaces that enabled it will stop working until you disable it there.',
    confirmLabel: 'Remove',
    danger: true,
  }))) return
  selfHostError.value = null
  selfHostBusy.value = { ...selfHostBusy.value, [name]: true }
  try {
    await orgProviders.remove(name)
    if (activeRegistration.value?.provider.name === name) activeRegistration.value = null
  } catch (e) {
    selfHostError.value = e instanceof Error ? e.message : String(e)
  } finally {
    selfHostBusy.value = { ...selfHostBusy.value, [name]: false }
  }
}

// Platform providers offering self-hosting that this org has not taken up yet.
const availableToSelfHost = computed(() =>
  providers.selfHostable.filter((p) => !orgProviders.isSelfHosted(p.name)),
)

// A card is a provider plus the resolved category metadata it belongs to.
// We carry the category on each card (rather than in a section header) so
// the grid can stay flat: categories become a chip inside the block and a
// filter control above it, instead of a separate section per category —
// which produced one header per provider when a category had a single entry.
interface ProviderCard extends ProviderDTO {
  categoryName: string
  categoryIcon: string | null
}

// Synthetic bucket name for providers that declare no category.
const OTHER = 'Other'

// allCards flattens providers into a single, stably-ordered list. Ordering
// matches the side-nav: registry categories first (by declared order), then
// ad-hoc categories alphabetically, then uncategorized ("Other") last; within
// a category, providers sort alphabetically by display name.
const allCards = computed<ProviderCard[]>(() => {
  const known = new Map(providers.categories.map((c) => [c.name, c]))
  const byCat = new Map<string, ProviderDTO[]>()
  const other: ProviderDTO[] = []
  for (const p of providers.items) {
    if (!p.category) {
      other.push(p)
      continue
    }
    const arr = byCat.get(p.category) ?? []
    arr.push(p)
    byCat.set(p.category, arr)
  }
  const names = [...byCat.keys()].sort((a, b) => {
    const ka = known.get(a)
    const kb = known.get(b)
    if (ka && !kb) return -1
    if (!ka && kb) return 1
    if (ka && kb) return (ka.order ?? 0) - (kb.order ?? 0) || a.localeCompare(b)
    return a.localeCompare(b)
  })
  const cards: ProviderCard[] = []
  for (const n of names) {
    const items = byCat.get(n)!.slice().sort((a, b) => a.displayName.localeCompare(b.displayName))
    for (const p of items) cards.push({ ...p, categoryName: n, categoryIcon: known.get(n)?.icon ?? null })
  }
  for (const p of other.sort((a, b) => a.displayName.localeCompare(b.displayName))) {
    cards.push({ ...p, categoryName: OTHER, categoryIcon: null })
  }
  return cards
})

// categoryChips is the ordered, de-duplicated list of categories present in
// the catalog — drives the filter row. Order follows allCards' first
// appearance so it lines up with the (now hidden) section ordering.
const categoryChips = computed(() => {
  const seen = new Map<string, string | null>()
  for (const c of allCards.value) {
    if (!seen.has(c.categoryName)) seen.set(c.categoryName, c.categoryIcon)
  }
  return [...seen.entries()].map(([name, icon]) => ({ name, icon }))
})

// Active filters. selectedCategory === null means "All".
const search = ref('')
const selectedCategory = ref<string | null>(null)

// filteredCards applies the search query (matched against display name,
// provider name, and category) and the active category chip.
const filteredCards = computed<ProviderCard[]>(() => {
  const q = search.value.trim().toLowerCase()
  return allCards.value.filter((c) => {
    if (selectedCategory.value && c.categoryName !== selectedCategory.value) return false
    if (!q) return true
    return (
      c.displayName.toLowerCase().includes(q) ||
      c.name.toLowerCase().includes(q) ||
      c.categoryName.toLowerCase().includes(q)
    )
  })
})

// The catalog renders as one flat grid, but self-managed providers — the ones
// this organization registered and runs itself — get their own labelled block
// at the top. They are a different kind of thing from the platform catalog: the
// org's own team operates them, so "who fixes this when it breaks" has a
// different answer, which matters more to a reader than any category grouping.
//
// Rather than a second <ul> (which would mean duplicating the whole card
// template), the sections are expressed as rows in the existing grid, with
// headers spanning the full width.
// orderedCards is filteredCards with the self-managed ones hoisted to the
// front, each group keeping the category ordering allCards established.
const orderedCards = computed<ProviderCard[]>(() => [
  ...filteredCards.value.filter((c) => providers.isSelfManaged(c)),
  ...filteredCards.value.filter((c) => !providers.isSelfManaged(c)),
])

// sectionHeaderFor returns the header to render immediately above a card, or
// null. Emitting the header from inside the card loop (rather than building a
// separate row list) keeps the card's own template untouched and its `p` in
// scope. Headers appear only when there is actually something to separate — an
// org with no self-managed providers sees the page exactly as before.
function sectionHeaderFor(card: ProviderCard, index: number): { title: string; subtitle: string } | null {
  const own = providers.isSelfManaged(card)
  const previous = index > 0 ? orderedCards.value[index - 1] : null
  const boundary = previous === null || providers.isSelfManaged(previous) !== own
  if (!boundary) return null
  // Nothing self-managed in view → no headers at all, not a lone one.
  if (!orderedCards.value.some((c) => providers.isSelfManaged(c))) return null
  return own
    ? { title: 'Self-managed', subtitle: 'Providers your organization registered and runs itself.' }
    : { title: 'Platform catalog', subtitle: 'Providers operated by faros.' }
}

function categoryIcon(name: string | null): unknown {
  if (!name) return fallbackCategoryIcon
  return categoryIcons[name] ?? fallbackCategoryIcon
}

// per-provider in-flight flag so Enable/Disable buttons can show a spinner
// without coupling to the global loading state.
const busy = ref<Record<string, boolean>>({})
const actionError = ref<string | null>(null)

// The Enable confirmation dialog is shown for one provider at a time. Null
// when closed. The user reviews permission claims here before the APIBinding
// is actually POSTed.
const dialogProvider = ref<ProviderDTO | null>(null)

// Always refetch on mount. The store's initial load happens at app boot
// (App.vue), but new CatalogEntry installs are common while the portal is
// open — users navigate here precisely to see what's now installed, so a
// stale cached list defeats the page's purpose. The store guards against
// concurrent calls so a no-op fast-path is safe.
onMounted(() => {
  providers.load()
  orgProviders.load()
  // Loaded up front rather than when the tab opens: the Self-Host actions must
  // never render enabled and then become disabled a moment later.
  orgProviders.loadInstallTargets()
})

function openEnableDialog(p: ProviderDTO) {
  actionError.value = null
  const missing = providers.missingDependencies(p)
  if (missing.length > 0) {
    actionError.value = `${p.displayName} requires ${providers.dependencyLabels(missing).join(', ')} to be enabled first.`
    return
  }
  dialogProvider.value = p
}

async function onDialogConfirm(accept: PermissionClaim[]) {
  const p = dialogProvider.value
  if (!p) return
  busy.value = { ...busy.value, [p.name]: true }
  actionError.value = null
  try {
    await providers.enable(p, accept)
    dialogProvider.value = null
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    const next = { ...busy.value }
    delete next[p.name]
    busy.value = next
  }
}

async function onDisable(p: ProviderDTO) {
  busy.value = { ...busy.value, [p.name]: true }
  actionError.value = null
  try {
    await providers.disable(p)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    const next = { ...busy.value }
    delete next[p.name]
    busy.value = next
  }
}

function dependencyNotice(p: ProviderDTO): string {
  const missing = providers.missingDependencies(p)
  if (missing.length === 0) return ''
  return `Requires ${providers.dependencyLabels(missing).join(', ')}.`
}
</script>

<template>
  <AppLayout>
    <div>
      <header class="mb-6">
        <h1 class="text-xl font-semibold text-text-primary flex items-center gap-2">
          <Puzzle class="h-5 w-5 text-accent" :stroke-width="1.75" />
          Providers
        </h1>
        <p class="mt-1 text-sm text-text-muted">
          Extensions registered with this faros instance. Click <strong>Enable</strong>
          to create an APIBinding in your workspace and unlock the provider's CRs;
          <strong>Open</strong> launches its UI in the portal.
        </p>
      </header>

      <div v-if="providers.error" class="mb-4 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2">
        <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
        <span>{{ providers.error }}</span>
      </div>

      <div v-if="actionError" class="mb-4 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2">
        <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
        <span>{{ actionError }}</span>
      </div>

      <!-- Catalog vs Self-Hosting. Hidden entirely when the hub has no
           org-provider support, so the tab never leads to a dead surface. -->
      <nav v-if="orgProviders.supported" class="k-tabs mb-5" aria-label="Provider sections">
        <button
          v-for="t in tabs"
          :key="t.key"
          type="button"
          class="k-tab"
          :class="{ 'k-tab--active': tab === t.key }"
          :aria-current="tab === t.key ? 'page' : undefined"
          @click="tab = t.key"
        >
          {{ t.label }}
          <span
            v-if="t.key === 'self-hosting' && orgProviders.items.length"
            class="k-tab__count"
          >{{ orgProviders.items.length }}</span>
        </button>
      </nav>

      <div v-if="providers.loading && !providers.loaded" class="text-sm text-text-muted">
        Loading providers&hellip;
      </div>

      <div v-else-if="providers.items.length === 0" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-text-muted">
        No providers installed yet.
        <div class="mt-2 text-xs">
          See <code>docs/providers.md</code> and <code>providers/quickstart/</code> for an example.
        </div>
      </div>

      <!-- ===== Self-Hosting tab ===== -->
      <div v-else-if="tab === 'self-hosting'" class="space-y-6">
        <p class="text-sm text-text-muted">
          Run a provider inside your own cluster instead of using the platform's copy. faros
          creates a workspace for it in your organization and gives you a credential scoped
          to that workspace only; you deploy the provider with Helm. Your workspaces then
          enable your copy exactly like any other provider.
        </p>
        <p class="-mt-3 text-[11px] text-text-muted">
          The cluster must be connected to faros as an edge first. faros reaches a
          self-hosted provider over that cluster's outbound tunnel — there is no route
          into your network from the platform side.
        </p>

        <div
          v-if="selfHostError"
          class="rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2"
        >
          <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
          <span>{{ selfHostError }}</span>
        </div>

        <!-- No cluster the hub can reach → say so, and say what to do about it.
             The Self-Host buttons below are disabled off the same condition, so
             this block is the explanation for them, not a separate warning. -->
        <div
          v-if="orgProviders.installTargetsLoaded && !canSelfHost"
          class="rounded-lg border border-warning/30 bg-warning-subtle px-3 py-2.5 text-sm text-warning flex items-start gap-2"
        >
          <AlertTriangle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
          <div class="min-w-0">
            <p>{{ orgProviders.installTargetsReason || 'no connected cluster to install into' }}</p>
            <router-link
              to="/providers/edges"
              class="mt-1 inline-flex items-center gap-1 text-[11px] font-medium underline"
            >
              Connect a Kubernetes cluster
              <ExternalLink class="h-3 w-3" :stroke-width="2" />
            </router-link>
          </div>
        </div>

        <!-- Which cluster a new provider lands in. Shown only when there is a
             choice to make: one eligible cluster needs no picker. -->
        <div
          v-else-if="eligibleEdges.length > 1"
          class="flex flex-wrap items-center gap-2 rounded-lg border border-border-subtle bg-surface-raised/60 px-3 py-2"
        >
          <label for="self-host-edge" class="text-[11px] font-medium text-text-secondary">
            Install into
          </label>
          <select
            id="self-host-edge"
            v-model="selectedEdgeKey"
            class="k-input w-auto px-2 py-1 text-[11px]"
          >
            <option v-for="t in eligibleEdges" :key="`${t.workspace}/${t.name}`" :value="`${t.workspace}/${t.name}`">
              {{ edgeLabel(t) }}
            </option>
          </select>
        </div>
        <p
          v-else-if="selectedEdge"
          class="text-[11px] text-text-muted"
        >
          Installing into <span class="font-medium text-text-secondary">{{ edgeLabel(selectedEdge) }}</span>.
        </p>

        <!-- Install details for whichever provider the user just acted on. -->
        <section
          v-if="activeRegistration"
          class="rounded-xl border border-accent/30 bg-surface-raised/60 p-5"
        >
          <div class="mb-4 flex items-start justify-between gap-3">
            <div>
              <h2 class="text-base font-semibold text-text-primary">
                {{ activeRegistration.provider.upgradeAvailable ? 'Upgrade' : 'Install' }}
                {{ activeRegistration.provider.name }}
              </h2>
              <p class="mt-0.5 text-[11px] text-text-muted">
                Run these in the cluster where
                {{ activeRegistration.provider.upgradeAvailable ? `${activeRegistration.provider.name} is running` : `you want ${activeRegistration.provider.name} to run` }}.
              </p>
            </div>
            <button
              type="button"
              class="k-btn k-btn--ghost p-1 text-text-muted transition-colors hover:text-accent"
              title="Close"
              @click="activeRegistration = null"
            >
              <X class="h-4 w-4" :stroke-width="2" />
            </button>
          </div>
          <SelfHostInstructions :registration="activeRegistration" />
        </section>

        <!-- What this org already runs itself. -->
        <section v-if="orgProviders.items.length">
          <h2 class="mb-2 text-xs font-semibold uppercase tracking-wider text-text-secondary">
            Running in your cluster
          </h2>
          <ul class="space-y-2">
            <li
              v-for="p in orgProviders.items"
              :key="p.name"
              class="flex flex-wrap items-center gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4"
            >
              <div class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
                <Server class="h-4 w-4 text-accent" :stroke-width="1.75" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <h3 class="truncate text-sm font-semibold text-text-primary">
                    {{ p.displayName || p.name }}
                  </h3>
                  <span
                    class="rounded-sm px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
                    :class="
                      p.registered
                        ? 'border border-success/30 bg-success-subtle text-success'
                        : 'border border-warning/30 bg-warning-subtle text-warning'
                    "
                  >
                    {{ p.registered ? 'Installed' : 'Awaiting install' }}
                  </span>
                  <span
                    v-if="p.upgradeAvailable"
                    class="rounded-sm border border-warning/30 bg-warning-subtle px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-warning"
                  >
                    Update available
                  </span>
                </div>
                <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">
                  {{ p.name }}<span v-if="p.version"> · {{ p.version }}</span><span
                    v-if="p.upgradeAvailable && p.installedVersion && p.availableVersion"
                  > · {{ p.installedVersion }} &rarr; {{ p.availableVersion }}</span>
                </p>
                <!-- The gap between "workspace exists" and "chart installed" is
                     where a half-finished install sits; say so explicitly. -->
                <p v-if="!p.registered" class="mt-1 text-[11px] text-text-muted">
                  The workspace is ready but the provider has not registered itself yet.
                  Run the install steps, then this flips to Installed.
                </p>
                <p v-else-if="p.upgradeAvailable" class="mt-1 text-[11px] text-text-muted">
                  A newer chart is published. Upgrade shows one command that keeps the
                  Helm values you installed with.
                </p>
              </div>
              <div class="flex items-center gap-2">
                <!-- Upgrade opens the same instructions panel; the panel leads
                     with the upgrade command when the hub says one is due. -->
                <button
                  v-if="p.upgradeAvailable"
                  type="button"
                  class="k-btn k-btn--primary px-2.5 py-1 text-[11px] disabled:opacity-60"
                  :disabled="!!selfHostBusy[p.name]"
                  @click="showInstructions(p.name)"
                >
                  <Loader2 v-if="selfHostBusy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <ArrowUpCircle v-else class="h-3 w-3" :stroke-width="2" />
                  Upgrade
                </button>
                <button
                  type="button"
                  class="k-btn k-btn--ghost px-2.5 py-1 text-[11px] text-text-muted transition-colors hover:text-accent disabled:opacity-60"
                  :disabled="!!selfHostBusy[p.name]"
                  @click="showInstructions(p.name)"
                >
                  <Loader2 v-if="selfHostBusy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <RefreshCw v-else class="h-3 w-3" :stroke-width="2" />
                  Install details
                </button>
                <button
                  type="button"
                  class="k-btn k-btn--danger px-2.5 py-1 text-[11px] disabled:opacity-60"
                  :disabled="!!selfHostBusy[p.name]"
                  @click="removeSelfHosted(p.name)"
                >
                  <Trash2 class="h-3 w-3" :stroke-width="2" />
                  Remove
                </button>
              </div>
            </li>
          </ul>
        </section>

        <!-- What could be self-hosted but isn't yet. -->
        <section>
          <h2 class="mb-2 text-xs font-semibold uppercase tracking-wider text-text-secondary">
            Available to self-host
          </h2>
          <div
            v-if="availableToSelfHost.length === 0"
            class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-sm text-text-muted"
          >
            {{
              orgProviders.items.length
                ? 'You are already self-hosting every provider that offers it.'
                : 'No provider in this catalog publishes a self-hosting recipe yet.'
            }}
          </div>
          <ul v-else class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <li
              v-for="p in availableToSelfHost"
              :key="p.name"
              class="rounded-xl border border-border-subtle bg-surface-raised/60 p-4 transition-colors hover:border-accent/30"
            >
              <div class="flex items-start gap-3">
                <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
                  <img v-if="p.iconURL" :src="p.iconURL" alt="" class="h-6 w-6" @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')" />
                  <Puzzle v-else class="h-5 w-5 text-text-muted" :stroke-width="1.75" />
                </div>
                <div class="min-w-0 flex-1">
                  <h3 class="truncate text-sm font-semibold text-text-primary">{{ p.displayName }}</h3>
                  <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">
                    {{ p.name }}<span v-if="p.version"> · {{ p.version }}</span>
                  </p>
                </div>
              </div>
              <p v-if="p.description" class="mt-3 line-clamp-3 text-[11px] leading-relaxed text-text-muted">
                {{ p.description }}
              </p>
              <div class="mt-4 flex items-center gap-2">
                <button
                  type="button"
                  class="k-btn k-btn--primary px-2.5 py-1 text-[11px] disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="!!selfHostBusy[p.name] || !canSelfHost"
                  :title="canSelfHost ? undefined : (orgProviders.installTargetsReason ?? 'no connected cluster to install into')"
                  @click="selfHost(p)"
                >
                  <Loader2 v-if="selfHostBusy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Server v-else class="h-3 w-3" :stroke-width="2" />
                  Self-host
                </button>
                <a
                  v-if="p.selfHostingDocsURL"
                  :href="p.selfHostingDocsURL"
                  target="_blank"
                  rel="noreferrer noopener"
                  class="inline-flex items-center gap-1 text-[11px] text-text-muted hover:text-accent"
                >
                  Docs
                  <ExternalLink class="h-3 w-3" :stroke-width="2" />
                </a>
              </div>
            </li>
          </ul>
        </section>
      </div>

      <!-- ===== Catalog tab ===== -->
      <div v-else>
        <!-- Search + category filter. The grid stays flat (one card per
             provider); categories are a filter here and a chip on each card
             rather than a per-category section header. -->
        <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div class="relative w-full sm:max-w-xs">
            <Search class="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-text-muted" :stroke-width="1.75" />
            <input
              v-model="search"
              type="search"
              placeholder="Search providers…"
              class="k-input w-full bg-surface-raised/60 py-1.5 pl-8 pr-3 text-sm"
            />
          </div>
          <div class="flex flex-wrap items-center gap-1.5">
            <button
              type="button"
              class="k-btn k-btn--ghost rounded-sm px-2.5 py-1 text-[11px] font-medium transition-colors"
              :class="
                selectedCategory === null
                  ? 'border-accent/40 bg-accent/10 text-accent'
                  : 'border-border-subtle text-text-muted hover:border-accent/30'
              "
              @click="selectedCategory = null"
            >
              All
            </button>
            <button
              v-for="chip in categoryChips"
              :key="chip.name"
              type="button"
              class="k-btn k-btn--ghost inline-flex items-center gap-1 rounded-sm px-2.5 py-1 text-[11px] font-medium transition-colors"
              :class="
                selectedCategory === chip.name
                  ? 'border-accent/40 bg-accent/10 text-accent'
                  : 'border-border-subtle text-text-muted hover:border-accent/30'
              "
              @click="selectedCategory = selectedCategory === chip.name ? null : chip.name"
            >
              <component :is="categoryIcon(chip.icon)" class="h-3 w-3" :stroke-width="2" />
              {{ chip.name }}
            </button>
          </div>
        </div>

        <div
          v-if="filteredCards.length === 0"
          class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-sm text-text-muted"
        >
          No providers match your search.
        </div>

        <ul v-else class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <template v-for="(p, i) in orderedCards" :key="p.name">
          <!-- Full-width section divider inside the same grid, so the cards
               below it keep their column alignment. -->
          <li v-if="sectionHeaderFor(p, i)" class="col-span-full mt-2 first:mt-0">
            <h2 class="text-xs font-semibold uppercase tracking-wider text-text-secondary">
              {{ sectionHeaderFor(p, i)!.title }}
            </h2>
            <p class="mt-0.5 text-[11px] text-text-muted">{{ sectionHeaderFor(p, i)!.subtitle }}</p>
          </li>
          <li
            class="rounded-xl border border-border-subtle bg-surface-raised/60 p-4 transition-colors hover:border-accent/30"
          >
          <div class="flex items-start gap-3">
            <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
              <img v-if="p.iconURL" :src="p.iconURL" alt="" class="h-6 w-6" @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')" />
              <Puzzle v-else class="h-5 w-5 text-text-muted" :stroke-width="1.75" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <h2 class="truncate text-sm font-semibold text-text-primary">{{ p.displayName }}</h2>
                <span
                  class="rounded-sm px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
                  :class="
                    !p.ready
                      ? 'border border-border-default bg-surface-overlay text-text-muted'
                      : p.builtinRoute
                        ? 'border border-border-default bg-surface-overlay text-text-secondary'
                        : providers.isDisabling(p.name)
                          ? 'border border-warning/30 bg-warning-subtle text-warning'
                          : providers.hasStaleClaims(p.name)
                            ? 'border border-warning/30 bg-warning-subtle text-warning'
                            : providers.isEnabled(p.name)
                              ? 'border border-accent/30 bg-accent/10 text-accent'
                              : providers.hasMissingDependencies(p)
                                ? 'border border-warning/30 bg-warning-subtle text-warning'
                                : 'border border-success/30 bg-success-subtle text-success'
                  "
                >
                  {{ !p.ready ? 'Pending' : p.builtinRoute ? 'Built-in' : providers.isDisabling(p.name) ? 'Disabling' : providers.hasStaleClaims(p.name) ? 'Degraded' : providers.isEnabled(p.name) ? 'Enabled' : providers.hasMissingDependencies(p) ? 'Blocked' : 'Available' }}
                </span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">{{ p.name }}<span v-if="p.version"> · {{ p.version }}</span></p>
            </div>
          </div>

          <!-- CatalogEntry.spec.description. Clamped so a long blurb can't make
               one card in the grid taller than its row neighbours. -->
          <p v-if="p.description" class="mt-3 line-clamp-3 text-[11px] leading-relaxed text-text-muted">
            {{ p.description }}
          </p>

          <!-- A disable that kcp cannot finish. The binding is gone from the
               user's point of view the moment they click Disable, but kcp
               first cascade-deletes every CR of the bound APIs — and a CR
               finalizer whose controller is gone holds that open forever.
               kcp's condition message names the exact finalizer/resources, so
               show it verbatim; it is the only actionable clue anywhere. -->
          <div
            v-if="providers.isDisabling(p.name) && providers.deletionBlocked(p.name)"
            class="mt-3 rounded-md border border-warning/30 bg-warning-subtle p-2.5"
          >
            <div class="flex items-center gap-1.5 text-[10px] font-semibold text-warning">
              <AlertTriangle class="h-3 w-3 flex-shrink-0" :stroke-width="2" />
              Disable is stuck
            </div>
            <p class="mt-1 text-[10px] leading-relaxed text-warning">
              {{ providers.deletionBlocked(p.name) }}
            </p>
            <p class="mt-1.5 text-[10px] leading-relaxed text-text-muted">
              Some of this provider's resources are still waiting to be cleaned
              up. If this doesn't resolve on its own, delete the resources listed
              above (or remove their finalizers) to let the disable finish.
            </p>
          </div>

          <!-- A provider whose claims point at a replaced dependency keeps
               reporting Enabled and healthy while seeing none of the resources
               it claims, so this has to say what happened AND what to do —
               there is no other symptom until something downstream fails for
               reasons that look unrelated. -->
          <div
            v-if="providers.hasStaleClaims(p.name)"
            class="mt-3 rounded-md border border-warning/30 bg-warning-subtle p-2.5"
          >
            <div class="flex items-center gap-1.5 text-[10px] font-semibold text-warning">
              <AlertTriangle class="h-3 w-3 flex-shrink-0" :stroke-width="2" />
              Claims a replaced provider
            </div>
            <ul class="mt-1 space-y-0.5 text-[10px] leading-relaxed text-warning">
              <li v-for="c in providers.staleClaims(p.name)" :key="c.group + '/' + c.resource">
                <code class="font-mono">{{ c.resource }}.{{ c.group }}</code> still points at the
                copy this workspace no longer uses.
              </li>
            </ul>
            <p class="mt-1.5 text-[10px] leading-relaxed text-text-muted">
              <template v-if="providers.staleClaims(p.name).every((c) => c.repointable)">
                Disable and enable it again to point it at the copy you now run.
              </template>
              <template v-else>
                This provider is run by the platform, so its claims are shared by every
                organization and re-enabling will not change them. Run your own copy of it
                alongside the provider it claims, or ask the platform operator to repoint it.
              </template>
            </p>
          </div>

          <div class="mt-3 flex flex-wrap items-center gap-2 text-[10px] text-text-muted">
            <button
              type="button"
              class="k-btn k-btn--ghost inline-flex items-center gap-1 px-1.5 py-0.5 text-[10px] transition-colors hover:text-accent"
              @click="selectedCategory = selectedCategory === p.categoryName ? null : p.categoryName"
            >
              <component :is="categoryIcon(p.categoryIcon)" class="h-3 w-3" :stroke-width="2" />
              {{ p.categoryName }}
            </button>
            <!-- Repeated on the card, not just the section header: a search or
                 category filter can leave a self-managed card sitting far from
                 its heading. -->
            <span
              v-if="providers.isSelfManaged(p)"
              class="rounded-md border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-accent"
              title="Registered and operated by your organization"
            >
              Self-managed
            </span>
            <span v-if="p.hasUI" class="rounded-md border border-border-subtle px-1.5 py-0.5">UI</span>
            <span v-if="p.hasBackend" class="rounded-md border border-border-subtle px-1.5 py-0.5">Backend</span>
            <span v-if="p.apiExportName" class="rounded-md border border-border-subtle px-1.5 py-0.5">API</span>
          </div>

          <div
            v-if="!providers.isEnabled(p.name) && dependencyNotice(p)"
            class="mt-3 rounded-lg border border-warning/30 bg-warning-subtle px-3 py-2 text-[11px] text-warning"
          >
            {{ dependencyNotice(p) }}
          </div>

          <div class="mt-4 flex items-center gap-2">
            <!-- Open: only when ready and has UI. Enableable providers
                 (those declaring an APIExport) must also be enabled for
                 this user. Builtin providers go to their in-tree Vue
                 route; third-party load via /providers/{name} →
                 ProviderFrame. -->
            <router-link
              v-if="p.hasUI && p.ready && (!p.apiExportName || providers.isEnabled(p.name))"
              :to="p.builtinRoute ? `/${p.builtinRoute}` : `/providers/${p.name}`"
              class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-accent transition-colors hover:bg-accent-subtle"
            >
              Open
              <ExternalLink class="h-3 w-3" :stroke-width="2" />
            </router-link>

            <!-- Enable / Disable: only when provider declares an APIExport -->
            <template v-if="p.apiExportName && p.ready">
              <!-- Mid-deletion: neither Enable (name still taken) nor Disable
                   (already deleting) is actionable, so say what's happening. -->
              <span
                v-if="providers.isDisabling(p.name)"
                class="inline-flex items-center gap-1 rounded-lg border border-warning/30 bg-warning-subtle px-2.5 py-1 text-[11px] font-medium text-warning"
              >
                <Loader2 class="h-3 w-3 animate-spin" :stroke-width="2" />
                Disabling&hellip;
              </span>
              <button
                v-else-if="!providers.isEnabled(p.name)"
                type="button"
                class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-success transition-colors hover:border-success/40 hover:bg-success-subtle disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!!busy[p.name] || providers.hasMissingDependencies(p)"
                :title="dependencyNotice(p)"
                @click="openEnableDialog(p)"
              >
                <Loader2 v-if="busy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                <Plus v-else class="h-3 w-3" :stroke-width="2" />
                Enable
              </button>
              <button
                v-else
                type="button"
                class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-text-muted transition-colors hover:border-danger/30 hover:text-danger disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!!busy[p.name]"
                @click="onDisable(p)"
              >
                <Loader2 v-if="busy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                <X v-else class="h-3 w-3" :stroke-width="2" />
                Disable
              </button>
            </template>

            <span v-if="!p.ready" class="text-[11px] text-text-muted/70">
              Provider is starting&hellip;
            </span>
          </div>
          </li>
          </template>
        </ul>
      </div>
    </div>

    <ProviderEnableDialog
      :provider="dialogProvider"
      @cancel="dialogProvider = null"
      @confirm="onDialogConfirm"
    />
  </AppLayout>
</template>
