<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import { useProvidersStore, type ProviderDTO, type PermissionClaim } from '@/stores/providers'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'
import { Puzzle, ExternalLink, AlertCircle, Plus, X, Loader2 } from 'lucide-vue-next'

const providers = useProvidersStore()

// Group the catalog cards by category to match the side-nav structure.
// Categories the hub knows (registry) appear first by their declared
// order; ad-hoc categories follow alphabetically; uncategorized providers
// fall into the synthetic "Other" bucket at the end.
const groupedItems = computed(() => {
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
  const groups = names.map((n) => ({
    name: n,
    icon: known.get(n)?.icon ?? null,
    items: byCat.get(n)!.slice().sort((a, b) => a.displayName.localeCompare(b.displayName)),
  }))
  if (other.length) {
    groups.push({
      name: 'Other',
      icon: null,
      items: other.sort((a, b) => a.displayName.localeCompare(b.displayName)),
    })
  }
  return groups
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
</script>

<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl">
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

      <div v-else class="space-y-6">
        <section v-for="group in groupedItems" :key="group.name">
          <h2 class="mb-2 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wider text-text-muted">
            <component :is="categoryIcon(group.icon)" class="h-3 w-3" :stroke-width="2" />
            {{ group.name }}
          </h2>
          <ul class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <li
              v-for="p in group.items"
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
                          : 'border border-success/30 bg-success-subtle text-success'
                  "
                >
                  {{ !p.ready ? 'Pending' : p.builtinRoute ? 'Built-in' : providers.isEnabled(p.name) ? 'Enabled' : 'Available' }}
                </span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">{{ p.name }}<span v-if="p.version"> · {{ p.version }}</span></p>
            </div>
          </div>

          <div class="mt-3 flex flex-wrap items-center gap-2 text-[10px] text-text-muted">
            <span v-if="p.hasUI" class="rounded-md border border-border-subtle px-1.5 py-0.5">UI</span>
            <span v-if="p.hasBackend" class="rounded-md border border-border-subtle px-1.5 py-0.5">Backend</span>
            <span v-if="p.apiExportName" class="rounded-md border border-border-subtle px-1.5 py-0.5">API</span>
          </div>

          <div class="mt-4 flex items-center gap-2">
            <!-- Open: only when ready and has UI. Builtin providers go
                 to their in-tree Vue route; third-party load via
                 /providers/{name} → ProviderFrame. -->
            <router-link
              v-if="p.hasUI && p.ready"
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
                :disabled="!!busy[p.name]"
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
        </section>
      </div>
    </div>

    <ProviderEnableDialog
      :provider="dialogProvider"
      @cancel="dialogProvider = null"
      @confirm="onDialogConfirm"
    />
  </AppLayout>
</template>
