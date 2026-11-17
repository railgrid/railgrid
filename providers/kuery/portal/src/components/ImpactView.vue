<script setup lang="ts">
import InlineNotification from '../portalkit/InlineNotification.vue'
import { portalHref } from '../portalkit/navigation'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ChevronDown } from 'lucide-vue-next'

import type { RailgridContext } from '../element'
import type { ObjectResult, QuerySpec } from '../api'
import {
  buildElements, IMPACT_RELATIONS, mountGraph, relationColor, relationMetadata, RELATION_DIR, RELATION_LABELS,
  themeStyle, type GraphHandle,
} from '../graph'
import { createKueryRequestContext, resourceLabel, safeHTTPStatus, useKueryApi } from '../kuery'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'

const props = defineProps<{ context: RailgridContext | null; anchor: ObjectResult; savedView: string }>()
const emit = defineEmits<{ back: []; inspect: [row: ObjectResult] }>()
const context = computed(() => props.context)
const { api, query } = useKueryApi(context, computed(() => props.savedView))
const result = ref<ObjectResult | null>(null)
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
const errorStatus = ref<number | null>(null)
const responseTruncated = ref(false)
const responseWarnings = ref<string[]>([])
// Fitted canvas labels become difficult to read on narrow screens. Choose the
// grouped text view initially there, without overriding a later user choice.
const representation = ref<'graph' | 'list'>(typeof window !== 'undefined' && window.matchMedia?.('(max-width: 600px)').matches ? 'list' : 'graph')
const impactRoot = ref<HTMLElement | null>(null)
const graphHost = ref<HTMLElement | null>(null)
const graphError = ref('')
const graphRetrying = ref(false)
const graphReady = ref(false)
let controller: AbortController | null = null
let loadGeneration = 0
let graph: GraphHandle | null = null
let generation = 0
const requestContext = computed(() => createKueryRequestContext(context.value))

const title = computed(() => {
  const metadata = props.anchor.object?.metadata ?? {}
  return `${metadata.namespace ? `${metadata.namespace}/` : ''}${metadata.name || '?'}`
})
const subtitle = computed(() => `Declared impact on ${props.anchor.cluster?.split('/').pop() || 'this edge'}`)
const relations = computed(() => Object.entries(result.value?.relations ?? {}).filter(([, rows]) => (rows ?? []).length > 0))
// Cytoscape reads the host's rendered CSS palette. Use the same document
// theme for its legend, and track context changes so Vue recomputes it.
const lightTheme = computed(() => {
  void props.context?.theme
  return typeof document !== 'undefined' && document.documentElement.classList.contains('light')
})
const legendRelations = computed(() => relations.value.map(([name]) => {
  const relation = relationMetadata(name)
  return relation
    ? { ...relation, name, displayColor: relationColor(relation, lightTheme.value) }
    : { name, legendLabel: name, description: 'Direction unknown - deletion impact is not established.', lineStyle: 'solid' as const, displayColor: 'var(--color-text-muted)' }
}))
const namespaceRelationBounded = computed(() => (result.value?.relations?.namespaced?.length ?? 0) >= 200)
function spec(): QuerySpec {
  const metadata = props.anchor.object?.metadata ?? {}; const version = props.anchor.object?.apiVersion || ''; const group = version.includes('/') ? version.split('/')[0] : ''
  const filter = { name: metadata.name, ...(props.anchor.object?.kind ? { groupKind: { apiGroup: group, kind: props.anchor.object.kind } } : {}), ...(metadata.namespace ? { namespace: metadata.namespace } : {}) }
  return { maxDepth: 5, cluster: props.anchor.cluster ? { name: props.anchor.cluster } : undefined, filter: { objects: [filter] }, objects: { id: true, cluster: true, relations: Object.fromEntries(IMPACT_RELATIONS.map(name => [name, name === 'namespaced' ? { limit: 200 } : {}])) } }
}

async function load(): Promise<void> {
  if (!api.value) return
  controller?.abort()
  const current = new AbortController()
  const requestGeneration = ++loadGeneration
  const request = requestContext.value
  const requestIdentity = request.identity
  const hostFetch = props.context?.fetch
  const savedView = props.savedView
  controller = current
  loading.value = true
  const isCurrent = (): boolean => {
    if (controller !== current || loadGeneration !== requestGeneration) return false
    return requestContext.value.identity === requestIdentity && props.context?.fetch === hostFetch && props.savedView === savedView
  }
  try {
    const status = await query(spec(), current.signal)
    if (!isCurrent()) return
    const nextResult = status.objects?.[0] ?? null
    if (!nextResult) { error.value = 'The object was not found; synchronization may still be catching up. Retry impact analysis or return to inventory.'; errorStatus.value = null; return }
    result.value = nextResult
    responseTruncated.value = !!status.incomplete
    responseWarnings.value = status.warnings ?? []
    loaded.value = true
    error.value = ''; errorStatus.value = null
  } catch (reason) {
    if (!isCurrent()) return
    if (reason instanceof DOMException && reason.name === 'AbortError') return
    error.value = 'Could not load impact analysis. Retry or return to inventory.'
    errorStatus.value = safeHTTPStatus(reason)
  } finally {
    if (isCurrent()) loading.value = false
  }
}

function destroyGraph(): void { generation += 1; graph?.destroy(); graph = null; graphReady.value = false }
async function mount(preserveError = false): Promise<void> {
  destroyGraph(); if (!preserveError) graphError.value = ''
  if (!result.value || !graphHost.value || representation.value !== 'graph') return
  const current = generation; const built = buildElements(result.value)
  try {
    const handle = await mountGraph(graphHost.value, built.elements, themeStyle(graphHost.value), id => { if (current !== generation) return; const row = built.nodeIndex[id]; if (row && id !== result.value?.id) emit('inspect', row) })
    if (current !== generation) return handle.destroy()
    graph = handle; graphReady.value = true; graphError.value = ''
    requestAnimationFrame(() => { if (current === generation && graph === handle) handle.fit() })
  } catch (reason) {
    if (current === generation) graphError.value = 'Could not display the graph. Retry the graph or use the List representation.'
  }
}

async function retryGraph(): Promise<void> {
  if (graphRetrying.value) return
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  graphRetrying.value = true
  try {
    await mount(true)
    await nextTick()
    if (focused && !focused.isConnected && document.activeElement === document.body) impactRoot.value?.querySelector<HTMLElement>('.k-resource-page__title')?.focus()
  } finally {
    graphRetrying.value = false
  }
}

watch(() => props.anchor, () => {
  const focused = typeof document !== 'undefined' ? document.activeElement : null
  const moveFocus = !!focused && !!impactRoot.value?.contains(focused) && focused.classList.contains('kuery-related')
  result.value = null; loaded.value = false; error.value = ''; errorStatus.value = null; void load()
  if (moveFocus) void nextTick(() => {
    if (!focused?.isConnected && document.activeElement === document.body) impactRoot.value?.querySelector<HTMLElement>('.k-resource-page__title')?.focus()
  })
}, { immediate: true })
watch([api, requestContext, () => props.context?.fetch, () => props.savedView], ([ready, current, hostFetch, view], [wasReady, previous, previousFetch, previousView]) => {
  const analysisIdentityChanged = view !== previousView || current.scopeIdentity !== previous.scopeIdentity || hostFetch !== previousFetch ||
    (!hostFetch && current.token !== previous.token)
  if (analysisIdentityChanged) {
    // A new view or authority may produce a different result or have different access.
    controller?.abort(); loadGeneration += 1
    result.value = null; loaded.value = false; loading.value = false; error.value = ''; errorStatus.value = null
    responseTruncated.value = false; responseWarnings.value = []
  }
  if (ready && (!wasReady || current.identity !== previous.identity || hostFetch !== previousFetch || view !== previousView)) void load()
})
watch([result, representation], () => nextTick(() => void mount()))
watch(() => props.context?.theme, () => { if (graph && graphHost.value) graph.restyle(themeStyle(graphHost.value)) })
onBeforeUnmount(() => { loadGeneration += 1; controller?.abort(); destroyGraph() })
</script>

<template>
  <div ref="impactRoot" class="kuery-impact">
    <ResourceBackLink :href="portalHref('/providers/kuery')" @back="emit('back')">Back to Kuery</ResourceBackLink>
    <ResourcePage :title="title" :kind="anchor.object?.kind" :subtitle="subtitle" :loaded="loaded" :loading="loading" :error="error" :stale="loaded && !!error" retryable @retry="load">
      <template #actions><div class="kuery-view-switch" role="group" aria-label="Impact representation"><button v-for="value in ['graph','list']" :key="value" type="button" class="k-btn k-btn--ghost kuery-view-btn" :aria-pressed="representation === value" @click="representation = value as 'graph' | 'list'">{{ value === 'graph' ? 'Graph' : 'List' }}</button></div></template>
      <template #body>
        <p v-if="representation === 'graph'" id="impact-help" class="meta">The highlighted node is the object named above. For directional relations, an arrow from A to B means deleting A can affect B. Lines without arrows show lateral associations or relationships with unknown direction; they do not imply deletion impact. Select a graph node to inspect it, or use List for named relationships and keyboard access.</p>
        <aside v-if="representation === 'graph' && legendRelations.length" class="kuery-impact-legend" aria-labelledby="impact-legend-title">
          <h2 id="impact-legend-title" class="kuery-workbench-title">Relation legend</h2>
          <dl class="legend">
            <div v-for="relation in legendRelations" :key="relation.name" class="legend-item">
              <dt><span class="legend-swatch" aria-hidden="true" :style="{ borderTopColor: relation.displayColor, borderTopStyle: relation.lineStyle }" /><span class="legend-label">{{ relation.legendLabel }}</span></dt>
              <dd>{{ relation.description }}</dd>
            </div>
          </dl>
        </aside>
        <p id="impact-bounds" class="meta">Bounded analysis: transitive relations stop at depth 5, and Namespace membership returns at most 200 related objects. Graph and list views show declared results within those bounds; they are not a complete relation traversal.</p>
        <div class="kuery-query-notices">
          <InlineNotification v-if="responseTruncated" tone="warning" message="Kuery reported response-level truncation for this query. That status does not identify relation-level bounds." />
          <InlineNotification v-if="responseWarnings.length" tone="warning">Kuery reported: {{ responseWarnings.join(' ') }} Relation-level bounds are listed above separately.</InlineNotification>
          <InlineNotification v-if="namespaceRelationBounded" tone="warning" message="Namespace membership reached its 200-object relation bound. Additional members may be omitted." />
        </div>
        <p v-if="loaded && !relations.length" class="kuery-read-state" role="status">No declared relationships were found within these bounds.</p>
        <div v-if="representation === 'graph'">
          <InlineNotification v-if="graphError" tone="error" action-label="Retry graph" action-busy-label="Retrying graph…" :action-busy="graphRetrying" @action="retryGraph" :message="graphError" />
          <div class="kuery-graph-controls" role="group" aria-label="Graph zoom controls">
            <button type="button" class="k-btn k-btn--ghost" :disabled="!graphReady" @click="graph?.zoomBy(1.25)">Zoom in</button>
            <button type="button" class="k-btn k-btn--ghost" :disabled="!graphReady" @click="graph?.zoomBy(0.8)">Zoom out</button>
            <button type="button" class="k-btn k-btn--ghost" :disabled="!graphReady" @click="graph?.fit()">Fit graph</button>
          </div>
          <div ref="graphHost" class="kuery-graph" role="img" :aria-label="`Impact visualization for ${title}`" :aria-describedby="legendRelations.length ? 'impact-help impact-bounds impact-legend-title' : 'impact-help impact-bounds'" />
        </div>
        <div v-else-if="relations.length" class="kuery-impact-list">
          <section v-for="[relation, related] in relations" :key="relation"><h2>{{ RELATION_LABELS[relation] || relation }} <span class="k-badge k-badge--muted k-badge--label">{{ RELATION_DIR[relation] === 'up' ? 'impacts this object' : RELATION_DIR[relation] === 'down' ? 'impacted by this object' : RELATION_DIR[relation] === 'lateral' ? 'related' : 'direction unknown' }}</span></h2><ul><li v-for="row in related" :key="row.id || resourceLabel(row)"><button type="button" class="kuery-related" @click="emit('inspect', row)">{{ resourceLabel(row) }}</button></li></ul></section>
        </div>
      </template>
    </ResourcePage>
    <details v-if="error && errorStatus !== null" class="k-resource-technical">
      <summary class="k-resource-technical__summary"><span class="k-resource-technical__summary-label">Technical details</span><ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" /></summary>
      <div class="k-resource-technical__body"><div class="k-resource-technical__content">Impact request returned HTTP {{ errorStatus }}.</div></div>
    </details>
  </div>
</template>
