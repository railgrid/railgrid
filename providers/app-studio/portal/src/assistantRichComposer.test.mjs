import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('projects canonical contentParts with bounded selection identity', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const [message] = assistantThreadItemsToMessages([{
    id: 'user-rich',
    turnID: 'run-rich',
    type: 'userMessage',
    status: 'completed',
    content: 'Review this',
    data: {
      skills: [{ id: 'project:review', name: 'Review', description: 'public', scope: 'project' }],
      contextResources: [{ provider: 'demo', resourceRef: { apiVersion: 'demo.example/v1', kind: 'Table', resource: 'tables', name: 'trips' } }],
      contentParts: [
        { type: 'text', text: 'Review ' },
        { type: 'skill', skillID: 'project:review' },
        { type: 'text', text: ' this ' },
        { type: 'resource', resourceIndex: 0 },
        { type: 'resource', resourceIndex: 3 },
        { type: 'skill', skillID: 'private:not-selected' },
      ],
    },
    sequence: 1,
    createdAt: '2026-08-10T00:00:00Z',
  }], 'demo')

  assert.deepEqual(message.metadata.assistantContentParts, [
    { type: 'text', text: 'Review ' },
    { type: 'skill', skillID: 'project:review' },
    { type: 'text', text: ' this ' },
    { type: 'resource', resourceIndex: 0 },
  ])
})

test('rich composer source keeps editor input plain and chip deletion atomic', async () => {
  const source = await readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8')
  assert.match(source, /contenteditable/)
  assert.match(source, /event\.key === 'Backspace'/)
  assert.match(source, /event\.key === 'Delete'/)
  assert.match(source, /range\.deleteContents\(\)/)
  assert.match(source, /event\.clipboardData\?\.getData\('text\/plain'\)/)
  assert.match(source, /const offsets = selectionOffsets\(\)[\s\S]*rangeForOffsets\(start, offsets\[1\]\)/)
  assert.match(source, /setCaretOffset\(start \+ text\.length, true\)/)
  assert.match(source, /composing\.value/)
  assert.match(source, /assistantSlashToken\(visibleTextFromDOM\(\), caret\)/)
  assert.doesNotMatch(source, /[\u2726\u25c7]/u)
})

test('rich composer presents a hoverable annotation preview with an in-pill clear control', async () => {
  const source = await readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8')
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const disclosure = await readFile(new URL('./AssistantMessageAnnotations.vue', import.meta.url), 'utf8')
  assert.match(source, /const localAnnotations = computed/)
  assert.match(source, /if \(part\.type !== 'annotation'\) editor\.append\(createChip\(part\)\)/)
  assert.match(source, /for \(const part of localParts\.value\) \{[\s\S]*if \(part\.type === 'annotation'\) append\(part\)/)
  assert.match(source, /<AssistantMessageAnnotations/)
  assert.match(source, /annotationDocumentId\?: string/)
  assert.match(source, /annotationPagePath\?: string/)
  assert.match(source, /unresolvedAnnotationIds\?: string\[\]/)
  assert.match(source, /:current-document-id="annotationDocumentId"/)
  assert.match(source, /:current-page-path="annotationPagePath"/)
  assert.match(source, /:unresolved-annotation-ids="unresolvedAnnotationIds"/)
  assert.match(source, /:rebind-across-documents="true"/)
  assert.match(disclosure, /currentDocumentId\?: string/)
  assert.match(disclosure, /props\.currentDocumentId\.trim\(\)/)
  assert.match(disclosure, /props\.rebindAcrossDocuments/)
  assert.match(disclosure, /annotation\.pagePath === props\.currentPagePath/)
  assert.match(appSource, /:annotation-document-id="developmentPreviewAnnotationDocumentID"/)
  assert.doesNotMatch(appSource, /:annotation-document-i-d=/)
  assert.match(source, /:clearable="true"/)
  assert.match(source, /@remove-all="removeAllAnnotations"/)
  assert.doesNotMatch(source, /@update-annotation=/)
  assert.doesNotMatch(source, /@remove-annotation=/)
  assert.match(disclosure, /@mouseenter="show"/)
  assert.match(disclosure, /@mouseleave="hide"/)
  assert.match(disclosure, /@focusin="show"/)
  assert.match(disclosure, /:aria-describedby="panelID"/)
  assert.match(disclosure, /role="tooltip"/)
  assert.match(disclosure, /aria-label="Clear annotations"/)
  assert.match(disclosure, /@click\.stop="removeAll"/)
  assert.match(disclosure, /class="group relative inline-flex max-w-full"/)
  assert.match(disclosure, /group-hover:w-6/)
  assert.match(disclosure, /group-hover:opacity-100/)
  assert.match(disclosure, /group-focus-within:opacity-100/)
  assert.doesNotMatch(disclosure, /Edit annotation/)
  assert.doesNotMatch(disclosure, /Remove annotation/)
  assert.doesNotMatch(disclosure, />Remove all</)
  assert.doesNotMatch(source, /dataset\.annotationID/)

  const { default: AssistantMessageAnnotations } = await vite.ssrLoadModule('/src/AssistantMessageAnnotations.vue')
  const html = await renderToString(createSSRApp(AssistantMessageAnnotations, {
    annotations: [{
      id: 'annotation-composer-1', comment: 'Adjust this control', documentID: 'old-document', pagePath: '/',
      viewport: { width: 1024, height: 768 }, target: { tag: 'button', text: 'Save' },
    }],
    currentDocumentId: 'new-document',
    clearable: true,
    disclosureID: 'composer-annotations',
  }))
  assert.match(html, /aria-describedby="composer-annotations-panel"/)
  assert.match(html, /role="tooltip"/)
  assert.match(html, /aria-label="Clear annotations"/)
  assert.match(html, /Stale preview/)
  assert.match(html, /Adjust this control/)

})

test('marks DOM-owned input before emitting props so queued sync preserves the caret', async () => {
  const source = await readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8')
  const marked = source.indexOf('lastRenderedSignature.value = stateSignature(content, parts, localSkills.value, localResources.value)')
  const modelUpdate = source.indexOf("emit('update:modelValue', content)")
  assert.ok(marked >= 0, 'input path should mark the DOM-owned signature')
  assert.ok(modelUpdate > marked, 'the signature must be marked before the parent prop update')
  assert.match(source, /renderParts\(parts, props\.modelValue, nextSkills, nextResources\)/)
})

test('clears detached selections before external rerenders and preserves the divergence gate', async () => {
  const source = await readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8')
  const renderStart = source.indexOf('function renderParts(')
  const renderEnd = source.indexOf('\n}\n\ninterface Segment', renderStart)
  assert.ok(renderStart >= 0 && renderEnd > renderStart, 'renderParts should remain a bounded helper')
  const renderBody = source.slice(renderStart, renderEnd)
  const clearSelection = renderBody.indexOf('savedSelection.value = null')
  const replaceChildren = renderBody.indexOf('editor.replaceChildren()')
  assert.ok(clearSelection >= 0, 'rerenders must invalidate saved native ranges')
  assert.ok(replaceChildren > clearSelection, 'saved ranges must be cleared before old nodes are detached')

  const syncStart = source.indexOf('function syncFromProps()')
  const syncEnd = source.indexOf('\n}\n\nwatch(', syncStart)
  assert.ok(syncStart >= 0 && syncEnd > syncStart, 'syncFromProps should remain a bounded helper')
  const syncBody = source.slice(syncStart, syncEnd)
  assert.match(syncBody, /const signature = stateSignature\(props\.modelValue, parts, nextSkills, nextResources\)/)
  assert.match(syncBody, /if \(signature !== lastRenderedSignature\.value\) \{[\s\S]*renderParts\(parts, props\.modelValue, nextSkills, nextResources\)/)
})
