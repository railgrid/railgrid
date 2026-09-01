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

test('projects the immutable receipt contract and rejects oversized/malformed receipts', async () => {
  const { projectAssistantAttachmentReceipt, MAX_ASSISTANT_ATTACHMENT_BYTES } = await vite.ssrLoadModule('/src/assistantAttachments.ts')
  const receipt = {
    id: 'att-1', filename: 'screen.png', contentType: 'image/png', sizeBytes: 12,
    sha256: 'A'.repeat(64), createdAt: '2026-08-31T00:00:00Z', draft: false,
  }
  assert.deepEqual(projectAssistantAttachmentReceipt(receipt), {
    id: 'att-1', filename: 'screen.png', contentType: 'image/png', sizeBytes: 12,
    sha256: 'a'.repeat(64), createdAt: '2026-08-31T00:00:00Z',
  })
  assert.equal(projectAssistantAttachmentReceipt({ ...receipt, sizeBytes: MAX_ASSISTANT_ATTACHMENT_BYTES + 1 }), null)
  assert.equal(projectAssistantAttachmentReceipt({ ...receipt, sizeBytes: 0 }), null)
  assert.equal(projectAssistantAttachmentReceipt({ ...receipt, sha256: 'not-a-digest' }), null)
  assert.equal(projectAssistantAttachmentReceipt({ ...receipt, createdAt: '' }), null)
})

test('accepts screenshot/text inputs while keeping server text limits visible to the portal', async () => {
  const {
    ASSISTANT_LARGE_PASTE_BYTES,
    assistantAttachmentIsSupported,
    assistantAttachmentMaxBytes,
    MAX_ASSISTANT_TEXT_ATTACHMENT_BYTES,
  } = await vite.ssrLoadModule('/src/assistantAttachments.ts')
  assert.equal(ASSISTANT_LARGE_PASTE_BYTES, 10 << 10)
  assert.equal(assistantAttachmentIsSupported({ name: 'screen.png', type: 'image/png' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'screen.webp', type: 'image/webp' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'screen.png', type: 'application/octet-stream' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'screen.JPEG', type: 'binary/octet-stream' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'animation.gif', type: 'image/gif' }), false)
  assert.equal(assistantAttachmentIsSupported({ name: 'notes.md', type: 'text/markdown' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'notes.md', type: 'application/octet-stream' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'notes.txt', type: 'binary/octet-stream' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'notes.txt', type: '' }), true)
  assert.equal(assistantAttachmentIsSupported({ name: 'notes.html', type: 'text/html' }), false)
  assert.equal(assistantAttachmentIsSupported({ name: 'data.json', type: 'application/json' }), false)
  assert.equal(assistantAttachmentIsSupported({ name: 'data.json', type: 'application/octet-stream' }), false)
  assert.equal(assistantAttachmentIsSupported({ name: 'notes.html', type: 'binary/octet-stream' }), false)
  assert.equal(assistantAttachmentIsSupported({ name: 'screen.png', type: 'application/x-png' }), false)
  assert.equal(assistantAttachmentMaxBytes({ name: 'notes.txt', type: 'text/plain' }), MAX_ASSISTANT_TEXT_ATTACHMENT_BYTES)
  assert.equal(assistantAttachmentMaxBytes({ name: 'notes.txt', type: '' }), MAX_ASSISTANT_TEXT_ATTACHMENT_BYTES)
})

test('text attachment previews stay bounded, use the first meaningful line, and render as escaped text', async () => {
  const {
    ASSISTANT_TEXT_PREVIEW_MAX_CHARS,
    readAssistantAttachmentTextPreview,
  } = await vite.ssrLoadModule('/src/assistantAttachments.ts')
  const source = new Blob(['\r\n  <b>Keep this literal</b>  \r\nsecond line\r\n'], { type: 'text/plain' })
  const file = { size: source.size, slice: (start, end) => source.slice(start, end) }
  assert.deepEqual(await readAssistantAttachmentTextPreview(file), {
    text: '<b>Keep this literal</b>',
    truncated: true,
  })

  const longSource = new Blob(['x'.repeat(ASSISTANT_TEXT_PREVIEW_MAX_CHARS + 20)], { type: 'text/plain' })
  const longFile = { size: longSource.size, slice: (start, end) => longSource.slice(start, end) }
  const longPreview = await readAssistantAttachmentTextPreview(longFile)
  assert.equal(longPreview.text, 'x'.repeat(ASSISTANT_TEXT_PREVIEW_MAX_CHARS))
  assert.equal(longPreview.truncated, true)

  const emoji = '😀'.repeat(200)
  const emojiSource = new Blob([emoji], { type: 'text/plain' })
  const emojiFile = { size: emojiSource.size, slice: (start, end) => emojiSource.slice(start, end) }
  const emojiPreview = await readAssistantAttachmentTextPreview(emojiFile)
  assert.equal(emojiPreview.text, '😀'.repeat(ASSISTANT_TEXT_PREVIEW_MAX_CHARS))
  assert.equal(emojiPreview.truncated, true)

  const previewSource = await readFile(new URL('./AssistantAttachmentTextPreview.vue', import.meta.url), 'utf8')
  assert.match(previewSource, /\{\{ preview\.text \}\}/)
  assert.doesNotMatch(previewSource, /v-html/)
})

test('derives attachment errors from the candidates that are still present', async () => {
  const {
    ASSISTANT_ATTACHMENT_RESOLUTION_ERROR,
    assistantAttachmentErrorMessage,
  } = await vite.ssrLoadModule('/src/assistantAttachments.ts')
  const first = { clientID: 'one', status: 'error', error: 'upload failed' }
  const second = { clientID: 'two', status: 'error', error: 'server rejected the file' }
  assert.equal(assistantAttachmentErrorMessage([first, second]), 'upload failed')
  assert.equal(assistantAttachmentErrorMessage([second]), 'server rejected the file')
  assert.equal(assistantAttachmentErrorMessage([{ clientID: 'one', status: 'error' }]), ASSISTANT_ATTACHMENT_RESOLUTION_ERROR)
  assert.equal(assistantAttachmentErrorMessage([{ clientID: 'one', status: 'ready' }]), '')
})

test('identifies missing receipts and limits startup recovery to precise attachment errors', async () => {
  const {
    assistantAttachmentReceiptsMatch,
    isAssistantAttachmentReceiptUnavailableError,
    staleAssistantAttachmentClientIDs,
  } = await vite.ssrLoadModule('/src/assistantAttachments.ts')
  const first = {
    id: 'att-one', filename: 'one.txt', contentType: 'text/plain', sizeBytes: 4,
    sha256: 'a'.repeat(64), createdAt: '2026-08-31T00:00:00Z',
  }
  const second = { ...first, id: 'att-two', filename: 'two.txt', sha256: 'b'.repeat(64) }
  const candidates = [
    { clientID: 'client-one', receipt: first, status: 'ready' },
    { clientID: 'client-two', receipt: second, status: 'ready' },
  ]
  assert.equal(assistantAttachmentReceiptsMatch(first, first), true)
  assert.deepEqual(staleAssistantAttachmentClientIDs(candidates, [first]), ['client-two'])
  const precise = Object.assign(new Error('attachment receipt expired'), { status: 404 })
  assert.equal(isAssistantAttachmentReceiptUnavailableError(precise), true)
  assert.equal(isAssistantAttachmentReceiptUnavailableError(Object.assign(new Error('project is unavailable'), { status: 404 })), false)
  assert.equal(isAssistantAttachmentReceiptUnavailableError(new TypeError('network lost')), false)
})

test('keeps upload protocol and durable-accept clearing explicit in the portal seams', async () => {
  const [api, composer, app] = await Promise.all([
    readFile(new URL('./api.ts', import.meta.url), 'utf8'),
    readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8'),
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  ])
  assert.match(api, /assistant\/attachments/)
  assert.match(api, /method: 'POST'/)
  assert.match(api, /'DELETE'/)
  assert.match(api, /listAssistantAttachments/)
  assert.match(api, /clientAttachmentID/)
  assert.match(composer, /ASSISTANT_LARGE_PASTE_BYTES/)
  assert.match(composer, /getAsFile\(\)/)
  assert.match(composer, /Retry attachment upload/)
  assert.match(composer, /bestEffortDeleteAttachment/)
  assert.match(composer, /isProjectAPINotFoundError/)
  assert.match(composer, /controller\.signal, clientID/)
  assert.match(composer, /update:attachmentsPending/)
  const acceptedStart = app.indexOf('startPostAccepted = true', app.indexOf('await api.startAssistantTurn'))
  const acceptedEnd = app.indexOf('\n      if (canonicalThreadID', acceptedStart)
  assert.ok(acceptedStart >= 0 && acceptedEnd > acceptedStart, 'normal accepted POST boundary must remain explicit')
  assert.match(app.slice(acceptedStart, acceptedEnd), /commitAttachments\([\s\S]*clearSelectedTurnAttachments\(\)/)
  const recoveredStart = app.indexOf('recoveredSameRequest = true')
  const recoveredEnd = app.indexOf('\n        } else {', recoveredStart)
  assert.ok(recoveredStart >= 0 && recoveredEnd > recoveredStart, 'recovered accepted request boundary must remain explicit')
  assert.match(app.slice(recoveredStart, recoveredEnd), /commitAttachments\([\s\S]*clearSelectedTurnAttachments\(\)/)
  assert.match(app, /bestEffortDeletePreProjectAttachment/)
  assert.match(app, /recoverPreProjectAttachmentReceipts/)
  assert.match(app, /clearPreProjectAttachments\(true\)/)
})

test('history renders image previews through the scoped API while text stays compact', async () => {
  const [{ default: AttachmentComponent }, { api }, app, apiSource] = await Promise.all([
    vite.ssrLoadModule('/src/AssistantMessageAttachments.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
    readFile(new URL('./api.ts', import.meta.url), 'utf8'),
  ])
  const originalGet = api.getAssistantAttachment
  const calls = []
  // The component and this test resolve the same Vite module instance. Keep
  // the fetch stub at the existing scoped API seam rather than bypassing it.
  assert.equal(typeof originalGet, 'function')
  api.getAssistantAttachment = async (...args) => {
    calls.push(args)
    return new Blob(['image-bytes'], { type: 'image/png' })
  }
  const image = {
    id: 'att-image', filename: 'screen.png', contentType: 'image/png', sizeBytes: 12,
    sha256: 'a'.repeat(64), createdAt: '2026-08-31T00:00:00Z',
  }
  const text = {
    id: 'att-text', filename: 'plan.txt', contentType: 'text/plain', sizeBytes: 42,
    sha256: 'b'.repeat(64), createdAt: '2026-08-31T00:00:00Z',
  }
  try {
    const html = await renderToString(createSSRApp(AttachmentComponent, {
      attachments: [image, text],
      ctx: { token: 'user-token' },
      projectName: 'demo',
    }))
    assert.match(html, /aria-busy="true"/)
    assert.match(html, /Loading screen\.png/)
    assert.match(html, /plan\.txt/)
    await Promise.resolve()
    assert.equal(calls.length, 1)
    assert.equal(calls[0][1], 'demo')
    assert.equal(calls[0][2], 'att-image')
    assert.ok(calls[0][3] instanceof AbortSignal)
  } finally {
    api.getAssistantAttachment = originalGet
  }
  assert.match(app, /<AssistantMessageAttachments[\s\S]*:ctx="props\.ctx"[\s\S]*:project-name="message\.projectID"/)
  assert.match(apiSource, /async getAssistantAttachment\(ctx: FarosContext \| null, name: string, attachmentID: string, signal\?: AbortSignal\)/)
  assert.match(apiSource, /cache: 'no-cache'[\s\S]*signal,/)
})

test('image preview lifecycle revokes object URLs and aborts stale scoped requests', async () => {
  const componentSource = await readFile(new URL('./AssistantMessageAttachments.vue', import.meta.url), 'utf8')
  assert.match(componentSource, /URL\.createObjectURL\(blob\)/)
  assert.match(componentSource, /URL\.revokeObjectURL\(preview\.url\)/)
  assert.match(componentSource, /URL\.revokeObjectURL\(url\)/)
  assert.match(componentSource, /new AbortController\(\)/)
  assert.match(componentSource, /api\.getAssistantAttachment\(props\.ctx, props\.projectName, attachment\.id, controller\.signal\)/)
  assert.match(componentSource, /onBeforeUnmount\(\(\) => \{[\s\S]*releasePreview\(attachmentID\)/)
})
