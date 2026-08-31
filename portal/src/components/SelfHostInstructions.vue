<script setup lang="ts">
import { computed, ref } from 'vue'
import MarkdownIt from 'markdown-it'
import { Copy, Check, Download, AlertTriangle, ExternalLink, ChevronRight, ArrowUpCircle } from 'lucide-vue-next'
import type { OrgProviderRegistration } from '@/stores/orgProviders'

// Renders the credential + Helm steps the hub generated for one self-hosted
// provider.
//
// The kubeconfig is shown once per fetch and is a live, long-lived credential
// for the provider's workspace, so the affordances here are deliberately about
// getting it out of the browser and into a file: download is the primary
// action, and the value stays masked until the user asks to see it.
const props = defineProps<{ registration: OrgProviderRegistration }>()

const revealed = ref(false)
const copied = ref<string | null>(null)

// The values the hub could not resolve — the only ones worth listing, since
// everything else is already correct in the command above.
const unresolvedValues = computed(
  () => props.registration.instructions?.values?.filter((v) => v.unresolved) ?? [],
)

// The hub decides whether an upgrade is due (installed vs. published chart
// version); this component only needs the verdict plus the rendered command.
const upgrade = computed(() => {
  const step = props.registration.instructions?.upgrade
  return props.registration.provider.upgradeAvailable && step ? step : null
})

// html: false is load-bearing, not a style choice. valuesDoc comes from a
// CatalogEntry, and for an org-owned provider that entry is written by whoever
// runs it — so this is tenant-authored content rendered in another tenant
// admin's browser. Disabling raw HTML keeps it text, not markup.
const md = new MarkdownIt({ html: false, linkify: true, breaks: false, typographer: false })
const defaultLinkOpen = md.renderer.rules.link_open
md.renderer.rules.link_open = (tokens, i, options, env, self) => {
  tokens[i].attrSet('target', '_blank')
  tokens[i].attrSet('rel', 'noopener noreferrer')
  return defaultLinkOpen ? defaultLinkOpen(tokens, i, options, env, self) : self.renderToken(tokens, i, options)
}

const valuesDocOpen = ref(false)
const valuesDocHTML = computed(() => {
  const doc = props.registration.instructions?.valuesDoc
  return doc ? md.render(doc) : ''
})

async function copy(key: string, value: string) {
  try {
    await navigator.clipboard.writeText(value)
    copied.value = key
    setTimeout(() => {
      if (copied.value === key) copied.value = null
    }, 1500)
  } catch {
    // Clipboard can be blocked (insecure context, permissions). The text is
    // on screen and selectable, so failing silently is better than an alert.
  }
}

function downloadKubeconfig() {
  const name = props.registration.instructions?.kubeconfigFilename
    ?? `${props.registration.provider.name}.kubeconfig`
  const blob = new Blob([props.registration.kubeconfig], { type: 'application/yaml' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="space-y-4">
    <!-- Upgrade, shown first and only when one is due. A running install needs
         none of the steps below — the namespace, credential Secret, and values
         all survive from the original install — so the one command is the whole
         path, and burying it under the install steps would read as "reinstall". -->
    <section
      v-if="upgrade"
      class="rounded-lg border border-accent/40 bg-accent/5 p-4"
    >
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <h4 class="flex items-center gap-1.5 text-sm font-semibold text-text-primary">
            <ArrowUpCircle class="h-4 w-4 text-accent" :stroke-width="2" />
            {{ upgrade.title }}
            <span
              v-if="registration.provider.installedChartVersion && registration.provider.availableChartVersion"
              class="font-mono text-[10px] font-normal text-text-muted"
            >
              v{{ registration.provider.installedChartVersion }} &rarr;
              v{{ registration.provider.availableChartVersion }}
            </span>
          </h4>
          <p v-if="upgrade.description" class="mt-0.5 text-[11px] text-text-muted">{{ upgrade.description }}</p>
        </div>
        <button
          type="button"
          class="k-btn k-btn--ghost inline-flex flex-shrink-0 items-center gap-1 px-2.5 py-1 text-[11px] text-text-muted transition-colors hover:text-accent"
          @click="copy('upgrade', upgrade.command)"
        >
          <component :is="copied === 'upgrade' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
          Copy
        </button>
      </div>
      <pre
        class="mt-3 overflow-x-auto rounded-md border border-border-subtle bg-surface-base p-3 font-mono text-[10px] leading-relaxed text-text-secondary"
      >{{ upgrade.command }}</pre>
      <p class="mt-2 text-[11px] text-text-muted">
        To change values at the same time, add <code class="font-mono text-text-secondary">--set</code> flags
        (or a values file) to this command — see the chart values reference below. The install
        steps that follow are for a fresh install and are not part of an upgrade.
      </p>
    </section>

    <!-- Credential. Step 1 of the flow even though the commands come after:
         nothing else works without the file on disk. -->
    <section class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h4 class="text-sm font-semibold text-text-primary">Credential</h4>
          <p class="mt-0.5 text-[11px] text-text-muted">
            Scoped to this provider's workspace only. Save it as
            <code class="font-mono text-text-secondary">{{
              registration.instructions?.kubeconfigFilename ?? `${registration.provider.name}.kubeconfig`
            }}</code>
            before running the commands below.
          </p>
        </div>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="k-btn k-btn--primary px-2.5 py-1 text-[11px]"
            @click="downloadKubeconfig"
          >
            <Download class="h-3 w-3" :stroke-width="2" />
            Download
          </button>
          <button
            type="button"
            class="k-btn k-btn--ghost px-2.5 py-1 text-[11px] text-text-muted transition-colors hover:text-accent"
            @click="revealed = !revealed"
          >
            {{ revealed ? 'Hide' : 'Show' }}
          </button>
          <button
            type="button"
            class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] text-text-muted transition-colors hover:text-accent"
            @click="copy('kubeconfig', registration.kubeconfig)"
          >
            <component :is="copied === 'kubeconfig' ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
            Copy
          </button>
        </div>
      </div>
      <pre
        v-if="revealed"
        class="mt-3 max-h-56 overflow-auto rounded-md border border-border-subtle bg-surface-base p-3 font-mono text-[10px] leading-relaxed text-text-secondary"
      >{{ registration.kubeconfig }}</pre>
    </section>

    <!-- Anything the hub could not fill in. Shown before the commands: these
         are the reasons a paste would not work. -->
    <div
      v-if="registration.instructions?.warnings?.length"
      class="rounded-lg border border-warning/30 bg-warning-subtle p-3"
    >
      <div class="flex items-center gap-1.5 text-[11px] font-semibold text-warning">
        <AlertTriangle class="h-3.5 w-3.5" :stroke-width="2" />
        Needs your attention
      </div>
      <ul class="mt-1.5 list-disc space-y-1 pl-4 text-[11px] text-warning">
        <li v-for="w in registration.instructions.warnings" :key="w">{{ w }}</li>
      </ul>
    </div>

    <template v-if="registration.instructions">
      <section
        v-for="(step, i) in registration.instructions.steps"
        :key="step.title"
        class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-4"
      >
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <h4 class="text-sm font-semibold text-text-primary">
              <span class="mr-1.5 text-text-muted">{{ i + 1 }}.</span>{{ step.title }}
            </h4>
            <p v-if="step.description" class="mt-0.5 text-[11px] text-text-muted">{{ step.description }}</p>
          </div>
          <button
            type="button"
            class="k-btn k-btn--ghost inline-flex flex-shrink-0 items-center gap-1 px-2.5 py-1 text-[11px] text-text-muted transition-colors hover:text-accent"
            @click="copy(step.title, step.command)"
          >
            <component :is="copied === step.title ? Check : Copy" class="h-3 w-3" :stroke-width="2" />
            Copy
          </button>
        </div>
        <pre
          class="mt-3 overflow-x-auto rounded-md border border-border-subtle bg-surface-base p-3 font-mono text-[10px] leading-relaxed text-text-secondary"
        >{{ step.command }}</pre>
      </section>

      <!-- Only the values the user still has to supply. The rest are explained
           in the chart's own values reference, which stays correct as the chart
           evolves — restating every --set here would be a second copy to drift. -->
      <section
        v-if="unresolvedValues.length"
        class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-4"
      >
        <h4 class="text-sm font-semibold text-text-primary">You need to fill in</h4>
        <ul class="mt-2 space-y-1.5">
          <li
            v-for="v in unresolvedValues"
            :key="v.name"
            class="flex flex-wrap items-baseline gap-x-2 text-[11px]"
          >
            <code class="font-mono text-warning">{{ v.name }}</code>
            <span v-if="v.description" class="text-text-muted">— {{ v.description }}</span>
          </li>
        </ul>
      </section>

      <!-- The chart's own values reference, carried inline by the provider so
           it works offline and matches the installed version. Collapsed by
           default: it is a reference, not part of the install path. -->
      <section
        v-if="valuesDocHTML"
        class="rounded-lg border border-border-subtle bg-surface-overlay/60"
      >
        <button
          type="button"
          class="k-btn k-btn--ghost flex w-full items-center gap-2 rounded-none border-0 p-4 text-left"
          @click="valuesDocOpen = !valuesDocOpen"
        >
          <ChevronRight
            class="h-4 w-4 flex-shrink-0 text-text-muted transition-transform"
            :class="valuesDocOpen ? 'rotate-90' : ''"
            :stroke-width="2"
          />
          <span class="flex-1 text-sm font-semibold text-text-primary">
            All chart values and what they do
          </span>
          <span v-if="registration.instructions.chartVersion" class="font-mono text-[10px] text-text-muted">
            v{{ registration.instructions.chartVersion }}
          </span>
        </button>
        <!-- eslint-disable-next-line vue/no-v-html -- markdown-it runs with
             html:false, so the output contains no author-supplied markup. -->
        <div v-if="valuesDocOpen" class="chart-doc px-4 pb-4" v-html="valuesDocHTML" />
      </section>

      <a
        v-else-if="registration.instructions.docsURL"
        :href="registration.instructions.docsURL"
        target="_blank"
        rel="noreferrer noopener"
        class="inline-flex items-center gap-1 text-[11px] font-medium text-accent hover:underline"
      >
        All chart values and what they do
        <ExternalLink class="h-3 w-3" :stroke-width="2" />
      </a>
    </template>

    <p v-else class="text-[11px] text-text-muted">
      This provider publishes no install recipe. Deploy it yourself using the credential
      above — it is already scoped to this provider's workspace, so the provider will
      register itself once it starts.
    </p>
  </div>
</template>

<style scoped>
/* Styling for the provider-supplied Markdown. Plain CSS over design tokens
   (the portal's convention) rather than @apply, because the HTML is injected
   at runtime and carries no utility classes of its own. */
.chart-doc :deep(h1),
.chart-doc :deep(h2),
.chart-doc :deep(h3) {
  margin: 1rem 0 0.25rem;
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--color-text-secondary);
}
.chart-doc :deep(h1) {
  font-size: 0.8125rem;
  color: var(--color-text-primary);
}
.chart-doc :deep(h2) {
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.chart-doc :deep(p),
.chart-doc :deep(li) {
  font-size: 0.6875rem;
  line-height: 1.6;
  color: var(--color-text-muted);
}
.chart-doc :deep(ul),
.chart-doc :deep(ol) {
  margin: 0.375rem 0;
  padding-left: 1rem;
  list-style: disc;
}
.chart-doc :deep(a) {
  color: var(--color-accent);
}
.chart-doc :deep(a:hover) {
  text-decoration: underline;
}
.chart-doc :deep(code) {
  padding: 0 0.25rem;
  border-radius: 2px;
  background: var(--color-surface-base);
  font-family: ui-monospace, monospace;
  font-size: 0.625rem;
  color: var(--color-text-secondary);
}
.chart-doc :deep(pre) {
  margin: 0.5rem 0;
  padding: 0.75rem;
  overflow-x: auto;
  border: 1px solid var(--color-border-subtle);
  border-radius: 6px;
  background: var(--color-surface-base);
}
.chart-doc :deep(pre code) {
  padding: 0;
  background: transparent;
}
/* The values table is wide by nature — a long key or default must scroll
   inside its own container rather than push the install panel sideways. */
.chart-doc :deep(table) {
  display: block;
  max-width: 100%;
  overflow-x: auto;
  margin: 0.5rem 0;
  border-collapse: collapse;
  font-size: 0.625rem;
}
.chart-doc :deep(th) {
  padding: 0.25rem 0.5rem;
  border-bottom: 1px solid var(--color-border-subtle);
  text-align: left;
  font-weight: 600;
  color: var(--color-text-secondary);
  white-space: nowrap;
}
.chart-doc :deep(td) {
  padding: 0.25rem 0.5rem;
  border-bottom: 1px solid var(--color-border-subtle);
  vertical-align: top;
  color: var(--color-text-muted);
}
</style>
