<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import { useProvidersStore, type ProviderDTO, type PermissionClaim } from '@/stores/providers'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'
import { Puzzle, ExternalLink, AlertCircle, Plus, X, Loader2, Search } from 'lucide-vue-next'

const providers = useProvidersStore()

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
          <Puzzle class="h-5 w-5 text-accent" :stroke-width="2" />
          Providers
        </h1>
        <p class="mt-1 text-sm text-text-muted">
          Extensions registered with this kedge instance. Click <strong>Enable</strong>
          to create an APIBinding in your workspace and unlock the provider's CRs;
          <strong>Open</strong> launches its UI in the portal.
        </p>
      </header>

      <div v-if="providers.error" class="mb-4 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2">
        <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="2" />
        <span>{{ providers.error }}</span>
      </div>

      <div v-if="actionError" class="mb-4 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2">
        <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="2" />
        <span>{{ actionError }}</span>
      </div>

      <div v-if="providers.loading && !providers.loaded" class="text-sm text-text-muted">
        Loading providers&hellip;
      </div>

      <div v-else-if="providers.items.length === 0" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-text-muted">
        No providers installed yet.
        <div class="mt-2 text-xs">
          See <code>docs/providers.md</code> and <code>providers/quickstart/</code> for an example.
        </div>
      </div>

      <div v-else>
        <!-- Search + category filter. The grid stays flat (one card per
             provider); categories are a filter here and a chip on each card
             rather than a per-category section header. -->
        <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div class="relative w-full sm:max-w-xs">
            <Search class="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-text-muted" :stroke-width="2" />
            <input
              v-model="search"
              type="search"
              placeholder="Search providers…"
              class="w-full rounded-lg border border-border-subtle bg-surface-raised/60 py-1.5 pl-8 pr-3 text-sm text-text-primary placeholder:text-text-muted focus:border-accent/40 focus:outline-none"
            />
          </div>
          <div class="flex flex-wrap items-center gap-1.5">
            <button
              class="rounded-full border px-2.5 py-1 text-[11px] font-medium transition-colors"
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
              class="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-[11px] font-medium transition-colors"
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
          <li
            v-for="p in filteredCards"
            :key="p.name"
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
                  class="rounded-full px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
                  :class="
                    !p.ready
                      ? 'border border-border-default bg-surface-overlay text-text-muted'
                      : p.builtinRoute
                        ? 'border border-border-default bg-surface-overlay text-text-secondary'
                        : providers.isEnabled(p.name)
                          ? 'border border-accent/30 bg-accent/10 text-accent'
                          : providers.hasMissingDependencies(p)
                            ? 'border border-warning/30 bg-warning-subtle text-warning'
                            : 'border border-success/30 bg-success-subtle text-success'
                  "
                >
                  {{ !p.ready ? 'Pending' : p.builtinRoute ? 'Built-in' : providers.isEnabled(p.name) ? 'Enabled' : providers.hasMissingDependencies(p) ? 'Blocked' : 'Available' }}
                </span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">{{ p.name }}<span v-if="p.version"> · {{ p.version }}</span></p>
            </div>
          </div>

          <div class="mt-3 flex flex-wrap items-center gap-2 text-[10px] text-text-muted">
            <button
              type="button"
              class="inline-flex items-center gap-1 rounded-md border border-border-subtle px-1.5 py-0.5 transition-colors hover:border-accent/30 hover:text-accent"
              @click="selectedCategory = selectedCategory === p.categoryName ? null : p.categoryName"
            >
              <component :is="categoryIcon(p.categoryIcon)" class="h-3 w-3" :stroke-width="2" />
              {{ p.categoryName }}
            </button>
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
              class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-2.5 py-1 text-[11px] font-medium text-accent transition-colors hover:bg-accent/20"
            >
              Open
              <ExternalLink class="h-3 w-3" :stroke-width="2" />
            </router-link>

            <!-- Enable / Disable: only when provider declares an APIExport -->
            <template v-if="p.apiExportName && p.ready">
              <button
                v-if="!providers.isEnabled(p.name)"
                class="inline-flex items-center gap-1 rounded-lg border border-success/30 bg-success-subtle px-2.5 py-1 text-[11px] font-medium text-success transition-colors hover:bg-success/15 disabled:cursor-not-allowed disabled:opacity-60"
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
                class="inline-flex items-center gap-1 rounded-lg border border-border-default bg-surface-overlay px-2.5 py-1 text-[11px] font-medium text-text-muted transition-colors hover:border-danger/30 hover:text-danger disabled:cursor-not-allowed disabled:opacity-60"
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
