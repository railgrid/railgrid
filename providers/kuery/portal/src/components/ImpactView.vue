<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import type { FarosContext } from '../element'
import type { ObjectResult, QuerySpec } from '../api'
import {
  buildElements, IMPACT_RELATIONS, mountGraph, relationColor, RELATION_DIR, RELATION_LABELS, RELATION_METADATA,
  themeStyle, type GraphHandle,
} from '../graph'
import { createKueryRequestContext, errorMessage, resourceLabel, useKueryApi } from '../kuery'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'

const props = defineProps<{ context: FarosContext | null; anchor: ObjectResult }>()
const emit = defineEmits<{ back: []; inspect: [row: ObjectResult] }>()
const context = computed(() => props.context)
const { api, query } = useKueryApi(context)
const result = ref<ObjectResult | null>(null)
const loaded = ref(false)
const loading = ref(false)
const error = ref('')
const responseTruncated = ref(false)
const responseWarnings = ref<string[]>([])
const representation = ref<'graph' | 'list'>('graph')
const graphHost = ref<HTMLElement | null>(null)
const graphError = ref('')
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
const lightTheme = computed(() => props.context?.theme === 'light' || (
  props.context?.theme === 'system' && typeof document !== 'undefined' && document.documentElement.classList.contains('light')
))
const legendRelations = computed(() => RELATION_METADATA
  .filter(relation => (result.value?.relations?.[relation.name]?.length ?? 0) > 0)
  .map(relation => ({ ...relation, displayColor: relationColor(relation, lightTheme.value) })))
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
  controller = current
  loading.value = true; error.value = ''
  const isCurrent = (): boolean => {
    if (controller !== current || loadGeneration !== requestGeneration) return false
    return requestContext.value.identity === requestIdentity
  }
  try {
    const status = await query(spec(), current.signal)
    if (!isCurrent()) return
    result.value = status.objects?.[0] ?? null
    if (!result.value) throw new Error('The object was not found; synchronization may still be catching up')
    responseTruncated.value = !!status.incomplete
    responseWarnings.value = status.warnings ?? []
    loaded.value = true
  } catch (reason) {
    if (!isCurrent()) return
    const message = errorMessage(reason, 'Retry impact analysis or return to inventory.')
    if (message) error.value = message
  } finally {
    if (isCurrent()) loading.value = false
  }
}

function destroyGraph(): void { generation += 1; graph?.destroy(); graph = null }
async function mount(): Promise<void> {
  destroyGraph(); graphError.value = ''
  if (!result.value || !graphHost.value || representation.value !== 'graph') return
  const current = generation; const built = buildElements(result.value)
  try {
    const handle = await mountGraph(graphHost.value, built.elements, themeStyle(graphHost.value), id => { if (current !== generation) return; const row = built.nodeIndex[id]; if (row && id !== result.value?.id) emit('inspect', row) }, `${(context.value?.basePath || '').replace(/\/?$/, '/')}cytoscape.min.js`)
    if (current !== generation) return handle.destroy()
    graph = handle; requestAnimationFrame(() => { if (current === generation && graph === handle) handle.fit() })
  } catch (reason) {
    if (current === generation) graphError.value = errorMessage(reason, 'Retry the graph or use the List representation.')
  }
}

watch(() => props.anchor, () => { result.value = null; loaded.value = false; void load() }, { immediate: true })
watch([api, requestContext], ([ready, current], [wasReady, previous]) => {
  if (ready && (!wasReady || current.identity !== previous.identity)) void load()
})
watch([result, representation], () => nextTick(() => void mount()))
watch(() => props.context?.theme, () => { if (graph && graphHost.value) graph.restyle(themeStyle(graphHost.value)) })
onBeforeUnmount(() => { loadGeneration += 1; controller?.abort(); destroyGraph() })
</script>

<template>
  <div class="kuery-impact">
    <ResourceBackLink href="/providers/kuery" @back="emit('back')">Back to Kuery</ResourceBackLink>
    <ResourcePage :title="title" :kind="anchor.object?.kind" :subtitle="subtitle" :loaded="loaded" :loading="loading" :error="error" :stale="loaded && !!error" retryable @retry="load">
      <template #actions><div class="kuery-view-switch" role="group" aria-label="Impact representation"><button v-for="value in ['graph','list']" :key="value" type="button" class="k-btn k-btn--ghost kuery-view-btn" :aria-pressed="representation === value" @click="representation = value as 'graph' | 'list'">{{ value === 'graph' ? 'Graph' : 'List' }}</button></div></template>
      <template #body>
        <p class="meta">Arrows describe deletion impact: A→B means deleting A can break B. Activate a related graph node or list item to inspect it.</p>
        <aside v-if="legendRelations.length" class="kuery-impact-legend" aria-labelledby="impact-legend-title">
          <h2 id="impact-legend-title" class="kuery-workbench-title">Relation legend</h2>
          <dl class="legend">
            <div v-for="relation in legendRelations" :key="relation.name" class="legend-item">
              <dt><span class="legend-swatch" aria-hidden="true" :style="{ borderTopColor: relation.displayColor, borderTopStyle: relation.lineStyle }" />{{ relation.legendLabel }}</dt>
              <dd>{{ relation.description }}</dd>
            </div>
          </dl>
        </aside>
        <p id="impact-bounds" class="meta">Bounded analysis: transitive relations stop at depth 5, and Namespace membership returns at most 200 related objects. Graph and list views show declared results within those bounds; they are not a complete relation traversal.</p>
        <p v-if="responseTruncated" class="kuery-warning" role="status">Kuery reported response-level truncation for this query. That status does not identify relation-level bounds.</p>
        <p v-if="responseWarnings.length" class="kuery-warning" role="status">Kuery reported: {{ responseWarnings.join(' ') }} Relation-level bounds are listed above separately.</p>
        <p v-if="namespaceRelationBounded" class="kuery-warning" role="status">Namespace membership reached its 200-object relation bound. Additional members may be omitted.</p>
        <div v-if="representation === 'graph'">
          <div v-if="graphError" class="kuery-inline-error" role="alert">{{ graphError }} <button type="button" class="k-btn k-btn--ghost" @click="mount">Retry graph</button></div>
          <div ref="graphHost" class="kuery-graph" role="img" :aria-label="`Impact visualization for ${title}`" :aria-describedby="legendRelations.length ? 'impact-bounds impact-legend-title' : 'impact-bounds'" />
        </div>
        <div v-else-if="relations.length" class="kuery-impact-list">
          <section v-for="[relation, related] in relations" :key="relation"><h2>{{ RELATION_LABELS[relation] || relation }} <span class="k-badge">{{ RELATION_DIR[relation] === 'up' ? 'impacts this object' : RELATION_DIR[relation] === 'down' ? 'impacted by this object' : 'related' }}</span></h2><ul><li v-for="row in related" :key="row.id || resourceLabel(row)"><button type="button" class="kuery-related" @click="emit('inspect', row)">{{ resourceLabel(row) }}</button></li></ul></section>
        </div>
        <div v-else-if="loaded" class="kuery-read-state" role="status">No declared relationships were found for this object.</div>
      </template>
    </ResourcePage>
  </div>
</template>
