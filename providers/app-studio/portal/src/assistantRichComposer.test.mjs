import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'

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
