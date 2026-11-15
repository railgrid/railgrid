<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown, Play } from 'lucide-vue-next'

import type { RailgridContext } from '../element'
import type { QuerySpec } from '../api'
import { createKueryRequestContext, isNoEngagedEdgesError, safeHTTPStatus, useKueryApi } from '../kuery'
import { collectSchemaWords, createEditor, EXAMPLES, loadCodeMirror, type EditorHandle } from '../playground'
import FormSelect from '../portalkit/FormSelect.vue'

const props = defineProps<{ context: RailgridContext | null; active: boolean; savedView: string }>()
const context = computed(() => props.context)
const savedView = computed(() => props.savedView)
const requestContext = computed(() => createKueryRequestContext(context.value))
const { api, query } = useKueryApi(context, computed(() => props.savedView))
const editorHost = ref<HTMLElement | null>(null)
const fallback = ref(false)
const documentText = ref(JSON.stringify(EXAMPLES[0].spec, null, 2))
const example = ref('')
const result = ref('')
const submittedText = ref<string | null>(null)
const draftChanged = computed(() => submittedText.value !== null && documentText.value !== submittedText.value)
const error = ref('')
const notEngaged = ref(false)
const running = ref(false)
const editorError = ref('')
const errorStatus = ref<number | null>(null)
const runButton = ref<HTMLButtonElement | null>(null)
let editor: EditorHandle | null = null
let controller: AbortController | null = null
let schemaController: AbortController | null = null
let generation = 0
const exampleOptions = [{ value: '', label: 'Choose an example…' }, ...EXAMPLES.map((item, index) => ({ value: String(index), label: item.label }))]
const resultStatus = computed(() => running.value ? 'Running query…' : notEngaged.value ? 'Kuery is waiting for an edge to become available.' : error.value ? 'Query failed. Review the error.' : result.value ? 'Query completed.' : 'Results will appear after you run a query.')

async function mountEditor(): Promise<void> {
  if (!api.value) return
  const host = editorHost.value; if (!host || editor) return
  const current = ++generation; const uiBase = `${(context.value?.basePath || '').replace(/\/?$/, '/')}`
  schemaController?.abort(); schemaController = new AbortController()
  try {
    let words: string[] = []
    // The QuerySpec schema is a static asset beside the bundle, not a tenant
    // route — the provider serves exactly one of those. Fetch it the same way
    // the CodeMirror bundle below is fetched.
    try { const response = await fetch(`${uiBase}query-schema.json`, { credentials: 'same-origin', signal: schemaController.signal }); if (response.ok) words = collectSchemaWords(await response.json()) } catch { words = [] }
    const factory = await loadCodeMirror(`${uiBase}codemirror.bundle.js`, `${uiBase}codemirror.bundle.css`)
    if (current !== generation || !editorHost.value) return
    editor = createEditor(factory, editorHost.value, documentText.value, words, value => { documentText.value = value }); editor.refresh()
  } catch {
    if (current !== generation || !editorHost.value) return
    fallback.value = true; editorError.value = 'The enhanced editor could not load. Use the plain text editor below; query execution still works.'
  }
}
function chooseExample(value: string): void {
  example.value = value
  // The placeholder clears the selection, not the user's query draft.
  if (!value) return
  const index = Number(value)
  if (!Number.isInteger(index) || !EXAMPLES[index]) return
  documentText.value = JSON.stringify(EXAMPLES[index].spec, null, 2); editor?.setValue(documentText.value); editor?.refresh()
}
async function run(): Promise<void> {
  if (running.value) return
  const focused = typeof document === 'undefined' ? null : document.activeElement
  const raw = editor?.getValue() ?? documentText.value; documentText.value = raw
  let spec: QuerySpec
  try { spec = JSON.parse(raw) as QuerySpec } catch {
    error.value = 'Invalid JSON. Correct the QuerySpec and run it again.'; errorStatus.value = null; notEngaged.value = false; result.value = ''; submittedText.value = null
    await nextTick()
    if (focused && !focused.isConnected && document.activeElement === document.body) runButton.value?.focus()
    return
  }
  controller?.abort(); const current = new AbortController(); controller = current
  submittedText.value = raw
  running.value = true; error.value = ''; errorStatus.value = null; result.value = ''
  try {
    const response = await query(spec, current.signal)
    if (controller !== current) return
    result.value = JSON.stringify(response, null, 2); notEngaged.value = false
  }
  catch (reason) {
    if (controller !== current || (reason instanceof DOMException && reason.name === 'AbortError')) return
    if (isNoEngagedEdgesError(reason)) notEngaged.value = true
    else {
      notEngaged.value = false
      error.value = 'Could not run the query. Try again.'
      errorStatus.value = safeHTTPStatus(reason)
    }
  }
  finally {
    if (controller === current) {
      running.value = false
      await nextTick()
      if (focused && !focused.isConnected && document.activeElement === document.body) runButton.value?.focus()
    }
  }
}

watch(api, (ready, wasReady) => { if (ready && !wasReady) nextTick(() => void mountEditor()) })
// A draft and its results belong to the view and authority that produced them.
// Ignore token refreshes when the host owns authentication, but fence the
// fallback bearer because it is the authority used by that transport.
watch([() => requestContext.value.scopeIdentity, () => requestContext.value.user, () => props.savedView, () => context.value?.fetch, () => context.value?.fetch ? null : requestContext.value.token], () => {
  controller?.abort(); controller = null
  running.value = false; result.value = ''; submittedText.value = null
  error.value = ''; errorStatus.value = null; notEngaged.value = false
  example.value = ''
  documentText.value = JSON.stringify(EXAMPLES[0].spec, null, 2)
  editor?.setValue(documentText.value)
}, { flush: 'sync' })
watch(() => props.active, active => { if (active) nextTick(() => editor?.refresh()) })
onMounted(() => void mountEditor())
onBeforeUnmount(() => { controller?.abort(); controller = null; schemaController?.abort(); generation += 1; editor?.destroy(); editor = null })
</script>

<template>
  <section class="kuery-panel kuery-playground k-card" aria-labelledby="playground-title">
    <div class="kuery-panel-head"><div><h2 id="playground-title" class="kuery-panel-title">Query playground</h2><p class="meta">Write a Kuery QuerySpec against your workspace. Autocomplete uses the live schema; press Ctrl/Cmd-Space for suggestions.</p></div></div>
    <div class="kuery-toolbar"><label class="kuery-example"><span id="playground-example-label">Example</span><FormSelect :model-value="example" :options="exampleOptions" labelledby="playground-example-label" @update:model-value="chooseExample" /></label><button ref="runButton" type="button" class="k-btn k-btn--primary" :aria-disabled="running || undefined" :aria-busy="running || undefined" @click="run"><Play :size="14" :stroke-width="1.75" aria-hidden="true" />{{ running ? 'Running…' : 'Run query' }}</button></div>
    <div class="pg-split">
      <section aria-labelledby="query-editor-label"><h3 id="query-editor-label" class="kuery-workbench-title">QuerySpec editor</h3><div v-if="editorError" class="kuery-inline-error" role="status">{{ editorError }}</div><textarea v-if="fallback" v-model="documentText" class="k-input pg-fallback" aria-labelledby="query-editor-label" spellcheck="false" /><div v-else ref="editorHost" class="pg-editor" role="group" aria-labelledby="query-editor-label" /></section>
      <section aria-labelledby="query-results-label">
        <h3 id="query-results-label" class="kuery-workbench-title">Query results</h3>
        <p v-if="draftChanged" class="meta" role="status">The query has changed since it was run. Run it again to see results for the current query.</p>
        <p class="kuery-sr-only" role="status" aria-live="polite">{{ resultStatus }}</p>
        <div :aria-busy="running">
          <div v-if="notEngaged" class="kuery-read-state" role="status">
            <span>Kuery has not started syncing an edge in this workspace yet. Check again in a moment.</span>
            <button type="button" class="k-btn k-btn--ghost" :aria-disabled="running || undefined" :aria-busy="running || undefined" @click="run">{{ running ? 'Checking…' : 'Check again' }}</button>
          </div>
          <pre v-if="!notEngaged" class="pg-result" :class="{ error: !!error }">{{ error || result || (running ? 'Running query…' : '// Results appear here after you run a query.') }}</pre>
          <details v-if="error && errorStatus !== null" class="k-resource-technical"><summary class="k-resource-technical__summary"><span class="k-resource-technical__summary-label">Technical details</span><ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" /></summary><div class="k-resource-technical__body"><div class="k-resource-technical__content">Query request returned HTTP {{ errorStatus }}.</div></div></details>
        </div>
      </section>
    </div>
    <details class="k-resource-technical pg-docs">
      <summary class="k-resource-technical__summary">
        <span class="k-resource-technical__summary-label">API and access</span>
        <ChevronDown class="k-resource-technical__chevron" :size="14" aria-hidden="true" />
      </summary>
      <div class="k-resource-technical__body">
        <section class="k-resource-technical__section">
          <h3 class="k-resource-technical__section-title">Run a saved view</h3>
          <div class="k-resource-technical__content">
            <p>A query is the <code>run</code> verb on a SavedView. This editor runs your own scratch view, <code>{{ savedView || '…' }}</code>. Programmatic clients POST <code>{"input": {"query": …}}</code> to this hub path with an OIDC bearer token:</p>
            <pre class="k-resource-technical__pre">/clusters/&lt;workspace&gt;/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/&lt;name&gt;/run</pre>
            <p>You must be able to see the view and be granted <code>run</code> on it, so what you can query is exactly what your workspace RBAC allows.</p>
          </div>
        </section>
      </div>
    </details>
  </section>
</template>
