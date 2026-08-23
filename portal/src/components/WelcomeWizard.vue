<script setup lang="ts">
// First-run onboarding for a workspace that has nothing enabled yet.
//
// A fresh account lands on the dashboard with an empty grid and an empty side
// nav, because every provider ships as a CatalogEntry with an APIExport and
// must be explicitly enabled per workspace. The old empty state was a single
// grey line pointing at /providers, which assumes the reader already knows
// what a provider *is*. This replaces it with a three-step flow:
//
//   1. Concepts — what an org / workspace / provider / edge actually are.
//   2. Catalog — the enableable providers, each with its CatalogEntry
//      description, enabled in place through the normal consent dialog.
//   3. Done   — what changed and where to go next.
//
// Enabling always goes through ProviderEnableDialog: permission claims are a
// consent decision, and the welcome flow must not become a way to skip it.
//
// Dismissal is per workspace (workspaces are independent clusters, so setting
// one up says nothing about the next) and lives in localStorage — this is a
// presentation preference, not state worth a round-trip to the hub.

import { computed, ref } from 'vue'
import {
  AlertCircle,
  ArrowLeft,
  ArrowRight,
  Boxes,
  Building2,
  Check,
  CheckCircle2,
  ExternalLink,
  Loader2,
  Plus,
  Puzzle,
  Rocket,
  Server,
  Sparkles,
} from 'lucide-vue-next'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import { useProvidersStore, type ProviderDTO, type PermissionClaim } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'

const emit = defineEmits<{
  // Raised when the user skips or finishes. The dashboard swaps back to the
  // normal (now possibly populated) grid.
  dismiss: []
}>()

const providers = useProvidersStore()
const tenant = useTenantStore()

const step = ref(0)
const steps = ['Concepts', 'Providers', 'Done'] as const

// --- Step 1: concepts -------------------------------------------------------

// Deliberately four cards, in dependency order: you are in an org, which holds
// workspaces, which hold providers, one of which connects edges. Each line
// answers "what is it" and "why do I care", nothing more — the docs handle depth.
const concepts = [
  {
    icon: Building2,
    title: 'Organization',
    body: 'Who you share with. Members, billing, and access all hang off the org. You always have a personal one.',
  },
  {
    icon: Boxes,
    title: 'Workspace',
    body: 'An isolated control plane inside the org. Everything you create — edges, apps, resources — lives in exactly one workspace.',
  },
  {
    icon: Puzzle,
    title: 'Provider',
    body: 'An extension that adds APIs and a UI. Enabling one binds its APIs into this workspace and puts it in the side nav. Nothing is on by default.',
  },
  {
    icon: Server,
    title: 'Edge',
    body: 'A Kubernetes cluster or Linux host connected back to the hub through a reverse tunnel, so you can reach it without inbound firewall rules.',
  },
]

// --- Step 2: catalog --------------------------------------------------------

const catalog = computed(() => providers.enableable)
const enabledCount = computed(() => catalog.value.filter((p) => providers.isEnabled(p.name)).length)

// Per-provider in-flight flag, so one Enable spinning doesn't disable the rest.
const busy = ref<Record<string, boolean>>({})
const actionError = ref<string | null>(null)
const dialogProvider = ref<ProviderDTO | null>(null)

function categoryIcon(name: string | undefined): unknown {
  if (!name) return fallbackCategoryIcon
  const registered = providers.categories.find((c) => c.name === name)
  if (!registered?.icon) return fallbackCategoryIcon
  return categoryIcons[registered.icon] ?? fallbackCategoryIcon
}

function dependencyNotice(p: ProviderDTO): string {
  const missing = providers.missingDependencies(p)
  if (missing.length === 0) return ''
  return `Enable ${providers.dependencyLabels(missing).join(', ')} first.`
}

function openEnableDialog(p: ProviderDTO) {
  actionError.value = null
  if (providers.hasMissingDependencies(p)) {
    actionError.value = dependencyNotice(p)
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

// --- Navigation -------------------------------------------------------------

function next() {
  if (step.value < steps.length - 1) step.value += 1
}
function back() {
  if (step.value > 0) step.value -= 1
}

// The provider a "start here" link should point at once the user is done. The
// first thing they enabled beats an arbitrary pick, so the final step sends
// them somewhere that actually exists in their nav.
const firstEnabled = computed(() => catalog.value.find((p) => providers.isEnabled(p.name)) ?? null)
</script>

<template>
  <div class="mx-auto w-full max-w-3xl pb-10 pt-6">
    <!-- Hero -->
    <div class="mb-6 flex items-start gap-4">
      <div class="relative flex h-12 w-12 shrink-0 items-center justify-center">
        <div class="absolute inset-0 rounded-xl bg-accent/20 blur-md" />
        <div class="relative flex h-12 w-12 items-center justify-center rounded-xl border border-accent/25 bg-surface-overlay">
          <Rocket class="h-6 w-6 text-accent" :stroke-width="1.5" />
        </div>
      </div>
      <div class="flex-1">
        <h1 class="flex items-center gap-2 text-[18px] font-bold text-text-primary">
          Welcome to Faros
          <Sparkles class="h-4 w-4 text-accent" :stroke-width="1.75" />
        </h1>
        <p class="mt-1 text-[12px] text-text-muted">
          <span class="font-mono text-text-secondary">{{ tenant.activeWorkspace?.displayName || 'This workspace' }}</span>
          is empty. Two minutes here and it won't be — or
          <button type="button" class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-text-secondary underline decoration-dotted underline-offset-2 hover:bg-transparent hover:text-text-primary" @click="emit('dismiss')">
            skip and explore on your own</button>.
        </p>
      </div>
    </div>

    <!-- Step rail. Steps are clickable both ways: this is an intro, not a
         transaction, so there is no reason to trap someone on step 2. -->
    <ol class="mb-4 flex items-center gap-2">
      <li v-for="(label, i) in steps" :key="label" class="flex flex-1 items-center gap-2">
        <button
          type="button"
          class="k-btn k-btn--ghost flex items-center gap-2 border-0 bg-transparent p-0 text-left hover:bg-transparent"
          @click="step = i"
        >
          <span
            class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full border text-[10px] font-semibold transition-colors"
            :class="
              i < step
                ? 'border-accent/40 bg-accent/15 text-accent'
                : i === step
                  ? 'border-accent bg-accent text-on-accent'
                  : 'border-border-default bg-surface-overlay text-text-muted'
            "
          >
            <Check v-if="i < step" class="h-3 w-3" :stroke-width="2.5" />
            <template v-else>{{ i + 1 }}</template>
          </span>
          <span
            class="text-[11px] font-medium transition-colors"
            :class="i === step ? 'text-text-primary' : 'text-text-muted'"
          >
            {{ label }}
          </span>
        </button>
        <span v-if="i < steps.length - 1" class="h-px flex-1 bg-border-subtle" />
      </li>
    </ol>

    <div class="rounded-xl border border-border-default shadow-sm">
      <div class="rounded-xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur">
        <!-- ── Step 1: concepts ─────────────────────────────────────────── -->
        <template v-if="step === 0">
          <h2 class="text-[13px] font-semibold text-text-primary">The four things worth knowing</h2>
          <p class="mt-1 text-[11px] text-text-muted">
            Faros is a control plane you extend. Here is the whole vocabulary.
          </p>

          <ul class="mt-4 grid gap-3 sm:grid-cols-2">
            <li
              v-for="c in concepts"
              :key="c.title"
              class="rounded-xl border border-border-subtle bg-surface-overlay/40 p-4"
            >
              <div class="flex items-center gap-2">
                <component :is="c.icon" class="h-4 w-4 text-accent" :stroke-width="1.75" />
                <h3 class="text-[12px] font-semibold text-text-primary">{{ c.title }}</h3>
              </div>
              <p class="mt-1.5 text-[11px] leading-relaxed text-text-muted">{{ c.body }}</p>
            </li>
          </ul>

          <p class="mt-4 rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2 text-[11px] text-text-muted">
            Providers are enabled <strong class="font-semibold text-text-secondary">per workspace</strong>.
            Turning one on here leaves your other workspaces untouched, and you can turn it
            back off at any time from the catalog.
          </p>
        </template>

        <!-- ── Step 2: catalog ──────────────────────────────────────────── -->
        <template v-else-if="step === 1">
          <div class="flex items-start justify-between gap-4">
            <div>
              <h2 class="text-[13px] font-semibold text-text-primary">Pick what this workspace does</h2>
              <p class="mt-1 text-[11px] text-text-muted">
                Enable one or several. Each asks you to review what it can access before anything is bound.
              </p>
            </div>
            <span
              v-if="catalog.length"
              class="shrink-0 rounded-md border border-border-subtle bg-surface-overlay px-2 py-1 text-[10px] font-medium text-text-muted"
            >
              {{ enabledCount }} / {{ catalog.length }} enabled
            </span>
          </div>

          <div
            v-if="actionError"
            class="mt-4 flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-[11px] text-danger"
          >
            <AlertCircle class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
            <span>{{ actionError }}</span>
          </div>

          <!-- No enableable providers at all: an operator-side situation the
               user can't fix from here, so say so plainly instead of showing
               an empty list that reads like a loading bug. -->
          <div
            v-if="catalog.length === 0"
            class="mt-4 rounded-xl border border-border-subtle bg-surface-overlay/40 p-4 text-[11px] text-text-muted"
          >
            <div class="font-medium text-text-secondary">No providers are installed on this instance yet.</div>
            <div class="mt-1">
              Providers are installed by whoever operates this hub, as Helm charts that register a
              CatalogEntry. Until one is installed there is nothing to enable here.
            </div>
          </div>

          <ul v-else class="mt-4 space-y-2">
            <li
              v-for="p in catalog"
              :key="p.name"
              class="rounded-xl border bg-surface-overlay/40 p-4 transition-colors"
              :class="providers.isEnabled(p.name) ? 'border-accent/30' : 'border-border-subtle hover:border-accent/25'"
            >
              <div class="flex items-start gap-3">
                <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-raised">
                  <img
                    v-if="p.iconURL"
                    :src="p.iconURL"
                    alt=""
                    class="h-5 w-5"
                    @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')"
                  />
                  <Puzzle v-else class="h-4 w-4 text-text-muted" :stroke-width="1.75" />
                </div>

                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center gap-2">
                    <h3 class="text-[12px] font-semibold text-text-primary">{{ p.displayName }}</h3>
                    <span
                      v-if="p.category"
                      class="inline-flex items-center gap-1 rounded-sm border border-border-subtle px-1.5 py-px text-[9px] font-medium text-text-muted"
                    >
                      <component :is="categoryIcon(p.category)" class="h-2.5 w-2.5" :stroke-width="2" />
                      {{ p.category }}
                    </span>
                    <span
                      v-if="providers.isEnabled(p.name)"
                      class="inline-flex items-center gap-1 rounded-sm border border-accent/30 bg-accent/10 px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-accent"
                    >
                      <Check class="h-2.5 w-2.5" :stroke-width="2.5" />
                      Enabled
                    </span>
                  </div>

                  <!-- The description is the entire point of this screen: it is
                       what turns an opaque name into a decision. Fall back to
                       something honest rather than rendering an empty line. -->
                  <p class="mt-1 text-[11px] leading-relaxed text-text-muted">
                    {{ p.description || 'No description published by this provider.' }}
                  </p>

                  <p v-if="dependencyNotice(p) && !providers.isEnabled(p.name)" class="mt-1.5 text-[10px] text-warning">
                    {{ dependencyNotice(p) }}
                  </p>
                </div>

                <div class="shrink-0">
                  <router-link
                    v-if="providers.isEnabled(p.name) && p.hasUI"
                    :to="`/providers/${p.name}`"
                    class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-text-secondary transition-colors hover:text-accent"
                  >
                    Open
                    <ExternalLink class="h-3 w-3" :stroke-width="2" />
                  </router-link>
                  <span
                    v-else-if="providers.isEnabled(p.name)"
                    class="inline-flex items-center gap-1 text-[11px] font-medium text-accent"
                  >
                    <CheckCircle2 class="h-3.5 w-3.5" :stroke-width="2" />
                  </span>
                  <button
                    v-else
                    type="button"
                    class="k-btn k-btn--primary inline-flex items-center gap-1 px-2.5 py-1 text-[11px] disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="!!busy[p.name] || providers.hasMissingDependencies(p)"
                    :title="dependencyNotice(p)"
                    @click="openEnableDialog(p)"
                  >
                    <Loader2 v-if="busy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                    <Plus v-else class="h-3 w-3" :stroke-width="2" />
                    Enable
                  </button>
                </div>
              </div>
            </li>
          </ul>
        </template>

        <!-- ── Step 3: done ─────────────────────────────────────────────── -->
        <template v-else>
          <div class="flex items-start gap-3">
            <CheckCircle2
              class="mt-0.5 h-5 w-5 shrink-0"
              :class="enabledCount > 0 ? 'text-success' : 'text-text-muted'"
              :stroke-width="1.75"
            />
            <div>
              <h2 class="text-[13px] font-semibold text-text-primary">
                {{ enabledCount > 0 ? 'Your workspace is set up' : 'Nothing enabled yet — that’s fine' }}
              </h2>
              <p class="mt-1 text-[11px] leading-relaxed text-text-muted">
                <template v-if="enabledCount > 0">
                  {{ enabledCount }} provider{{ enabledCount === 1 ? '' : 's' }}
                  {{ enabledCount === 1 ? 'is' : 'are' }} now bound to this workspace. They appear in the
                  side nav, and any that publish a dashboard tile show up on the dashboard.
                </template>
                <template v-else>
                  You can enable providers whenever you want from the catalog. The dashboard stays
                  empty until at least one is on.
                </template>
              </p>
            </div>
          </div>

          <ul class="mt-4 space-y-2 text-[11px] text-text-muted">
            <li class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2">
              <Puzzle class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
              <span>
                <router-link to="/providers" class="font-medium text-accent hover:text-accent-hover">Providers</router-link>
                — the full catalog. Enable and disable anything, any time.
              </span>
            </li>
            <li v-if="firstEnabled?.hasUI" class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2">
              <ArrowRight class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
              <span>
                <router-link
                  :to="`/providers/${firstEnabled.name}`"
                  class="font-medium text-accent hover:text-accent-hover"
                >{{ firstEnabled.displayName }}</router-link>
                — jump straight into what you just enabled.
              </span>
            </li>
            <li class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2">
              <Boxes class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
              <span>
                <router-link to="/tenant" class="font-medium text-accent hover:text-accent-hover">Settings</router-link>
                — add more workspaces, or invite people to the org.
              </span>
            </li>
          </ul>
        </template>
      </div>

      <!-- Footer: same controls on every step so the escape hatch never moves. -->
      <div class="flex items-center justify-between gap-3 border-t border-border-subtle px-6 py-4">
        <button
          type="button"
          class="k-btn k-btn--ghost flex items-center gap-1.5 border-0 bg-transparent px-0 py-0 text-[11px] font-medium text-text-muted transition-colors hover:bg-transparent hover:text-text-secondary"
          @click="step === 0 ? emit('dismiss') : back()"
        >
          <ArrowLeft v-if="step > 0" class="h-3 w-3" :stroke-width="2" />
          {{ step === 0 ? 'Skip for now' : 'Back' }}
        </button>

        <button
          type="button"
          class="k-btn k-btn--primary group px-4 py-2 text-[12px] active:scale-[0.98]"
          @click="step === steps.length - 1 ? emit('dismiss') : next()"
        >
          <span>{{ step === steps.length - 1 ? 'Go to dashboard' : 'Continue' }}</span>
          <ArrowRight class="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" :stroke-width="2" />
        </button>
      </div>
    </div>

    <ProviderEnableDialog
      :provider="dialogProvider"
      @cancel="dialogProvider = null"
      @confirm="onDialogConfirm"
    />
  </div>
</template>
