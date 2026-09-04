<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Play } from 'lucide-vue-next'

import type { FarosContext } from '../element'
import type { QuerySpec } from '../api'
import { errorMessage, serviceBase, tenantHeaders, useKueryApi } from '../kuery'
import { collectSchemaWords, createEditor, EXAMPLES, loadCodeMirror, type EditorHandle } from '../playground'
import FormSelect from '../portalkit/FormSelect.vue'

const props = defineProps<{ context: FarosContext | null; active: boolean }>()
const context = computed(() => props.context)
const { api, query } = useKueryApi(context)
const editorHost = ref<HTMLElement | null>(null)
const fallback = ref(false)
const documentText = ref(JSON.stringify(EXAMPLES[0].spec, null, 2))
const example = ref('')
const result = ref('')
const error = ref('')
const running = ref(false)
const editorError = ref('')
let editor: EditorHandle | null = null
let controller: AbortController | null = null
let schemaController: AbortController | null = null
let generation = 0
const exampleOptions = [{ value: '', label: 'Choose an example…' }, ...EXAMPLES.map((item, index) => ({ value: String(index), label: item.label }))]
const resultStatus = computed(() => running.value ? 'Running query…' : error.value ? 'Query failed. Review the error and update the QuerySpec.' : result.value ? 'Query completed.' : 'Results will appear after you run a query.')

async function mountEditor(): Promise<void> {
  if (!api.value) return
  const host = editorHost.value; if (!host || editor) return
  const current = ++generation; const uiBase = `${(context.value?.basePath || '').replace(/\/?$/, '/')}`
  schemaController?.abort(); schemaController = new AbortController()
  try {
    let words: string[] = []
    try { const response = await fetch(`${serviceBase(context.value)}/api/query-schema`, { credentials: 'same-origin', headers: tenantHeaders(context.value), signal: schemaController.signal }); if (response.ok) words = collectSchemaWords(await response.json()) } catch { words = [] }
    const factory = await loadCodeMirror(`${uiBase}codemirror.bundle.js`, `${uiBase}codemirror.bundle.css`)
    if (current !== generation || !editorHost.value) return
    editor = createEditor(factory, editorHost.value, documentText.value, words); editor.refresh()
  } catch (reason) {
    fallback.value = true; editorError.value = errorMessage(reason, 'Use the plain text editor below; query execution still works.')
  }
}
function chooseExample(value: string): void {
  example.value = value; const index = Number(value)
  if (!Number.isInteger(index) || !EXAMPLES[index]) return
  documentText.value = JSON.stringify(EXAMPLES[index].spec, null, 2); editor?.setValue(documentText.value); editor?.refresh()
}
async function run(): Promise<void> {
  const raw = editor?.getValue() ?? documentText.value; documentText.value = raw
  let spec: QuerySpec
  try { spec = JSON.parse(raw) as QuerySpec } catch (reason) { error.value = `Invalid JSON: ${reason instanceof Error ? reason.message : String(reason)}. Correct the QuerySpec and run it again.`; result.value = ''; return }
  controller?.abort(); const current = new AbortController(); controller = current
  running.value = true; error.value = ''
  try { result.value = JSON.stringify(await query(spec, current.signal), null, 2) }
  catch (reason) { const message = errorMessage(reason, 'Check the QuerySpec, then run it again.'); if (message) error.value = message }
  finally { if (controller === current) running.value = false }
}

watch(api, (ready, wasReady) => { if (ready && !wasReady) nextTick(() => void mountEditor()) })
watch(() => props.active, active => { if (active) nextTick(() => editor?.refresh()) })
onMounted(() => void mountEditor())
onBeforeUnmount(() => { controller?.abort(); schemaController?.abort(); generation += 1; editor?.destroy(); editor = null })
</script>

<template>
  <section class="kuery-panel k-card" aria-labelledby="playground-title">
    <div class="kuery-panel-head"><div><h2 id="playground-title" class="kuery-panel-title">Query playground</h2><p class="meta">Write a Kuery QuerySpec against your workspace. Autocomplete uses the live schema; press Ctrl/Cmd-Space for suggestions.</p></div></div>
    <div class="kuery-toolbar"><label class="kuery-example"><span id="playground-example-label">Example</span><FormSelect :model-value="example" :options="exampleOptions" labelledby="playground-example-label" @update:model-value="chooseExample" /></label><button type="button" class="k-btn k-btn--primary" :disabled="running" @click="run"><Play :size="14" :stroke-width="1.75" aria-hidden="true" />{{ running ? 'Running…' : 'Run query' }}</button></div>
    <div class="pg-split">
      <section aria-labelledby="query-editor-label"><h3 id="query-editor-label" class="kuery-workbench-title">QuerySpec editor</h3><div v-if="editorError" class="kuery-inline-error" role="status">{{ editorError }}</div><textarea v-if="fallback" v-model="documentText" class="pg-fallback" aria-labelledby="query-editor-label" spellcheck="false" /><div v-else ref="editorHost" class="pg-editor" role="group" aria-labelledby="query-editor-label" /></section>
      <section aria-labelledby="query-results-label"><h3 id="query-results-label" class="kuery-workbench-title">Query results</h3><p class="kuery-sr-only" role="status" aria-live="polite">{{ resultStatus }}</p><pre class="pg-result" :class="{ error: !!error }">{{ error || result || '// Results appear here after you run a query.' }}</pre></section>
    </div>
    <details class="pg-docs"><summary>API and access</summary><p>Programmatic clients can POST the same QuerySpec to <code>{{ serviceBase(context) }}/api/query</code> with an OIDC bearer token. The hub scopes every request to the selected workspace.</p></details>
  </section>
</template>
