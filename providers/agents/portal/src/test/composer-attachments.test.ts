import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, onTestFinished, vi } from 'vitest'
import AIConversationComposer from '../agentkit/AIConversationComposer.vue'
import type { AgentKitComposerPart } from '../agentkit/composer'
import { MAX_ASSISTANT_ATTACHMENTS_PER_TURN } from '../agentkit/composerAttachments'

const ownerKey = 'test-owner'
const initialScope = 'new-conversation'

function mountComposer() {
  const host = document.createElement('div')
  document.body.append(host)
  const activeRun = ref(false)
  const contentParts = ref<AgentKitComposerPart[]>([])
  const scope = ref(initialScope)
  const composer = ref<InstanceType<typeof AIConversationComposer> | null>(null)
  const upload = vi.fn(async (file: File, _signal: AbortSignal, clientID: string) => ({
    id: clientID,
    filename: file.name,
    contentType: file.type,
    sizeBytes: file.size,
    sha256: 'a'.repeat(64),
    createdAt: '2026-01-01T00:00:00Z',
  }))
  const remove = vi.fn(async (_id: string) => {})
  const transport = { upload, delete: remove }
  const app = createApp(() => h(AIConversationComposer, {
    ref: composer,
    modelValue: '',
    skills: [],
    activeRun: activeRun.value,
    contentParts: contentParts.value,
    draftScopeKey: scope.value,
    attachmentOwnerKey: ownerKey,
    attachmentTransport: transport,
    'onUpdate:contentParts': (parts: AgentKitComposerPart[]) => { contentParts.value = parts },
  }))
  app.mount(host)
  onTestFinished(() => {
    app.unmount()
    host.remove()
  })

  return {
    activeRun, composer, contentParts, scope, upload, remove,
    async attach(name: string, body = 'file contents') {
      const input = host.querySelector<HTMLInputElement>('input[type="file"]')!
      const file = new File([body], name, { type: 'application/octet-stream' })
      Object.defineProperty(input, 'files', { configurable: true, value: [file] })
      input.dispatchEvent(new Event('change', { bubbles: true }))
      await nextTick()
    },
    chip(name: string) {
      return host.querySelector<HTMLElement>(`[data-agentkit-attachment][aria-label="${name}"]`)
    },
    chipCount: () => host.querySelectorAll('[data-agentkit-attachment]').length,
    action(label: string) {
      return host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)
    },
  }
}

it('keeps a single actionable failed chip when retry exceeds the attachment limit', async () => {
  const ui = mountComposer()
  ui.upload.mockRejectedValueOnce(new Error('Response lost'))
  await ui.attach('retry.bin')
  for (let index = 0; index < MAX_ASSISTANT_ATTACHMENTS_PER_TURN; index++) {
    await ui.attach(`file-${index}.bin`)
  }
  const retryLabel = 'Retry attachment upload retry.bin'
  const initialChip = ui.chip('retry.bin')
  expect(ui.contentParts.value).toHaveLength(MAX_ASSISTANT_ATTACHMENTS_PER_TURN)

  ui.action(retryLabel)!.click()
  await nextTick()
  expect(ui.chipCount()).toBe(MAX_ASSISTANT_ATTACHMENTS_PER_TURN + 1)
  expect(ui.chip('retry.bin')).toBe(initialChip)
  expect(initialChip?.textContent).toContain('A turn can contain at most')
  expect(ui.action(retryLabel)).not.toBeNull()
  expect(ui.upload).toHaveBeenCalledTimes(MAX_ASSISTANT_ATTACHMENTS_PER_TURN + 1)

  ui.action('Remove attachment file-0.bin')!.click()
  await vi.waitFor(() => expect(ui.contentParts.value).toHaveLength(MAX_ASSISTANT_ATTACHMENTS_PER_TURN - 1))
  ui.action(retryLabel)!.click()
  await vi.waitFor(() => expect(ui.contentParts.value).toHaveLength(MAX_ASSISTANT_ATTACHMENTS_PER_TURN))
  expect(ui.upload.mock.lastCall?.[2]).toBe(ui.upload.mock.calls[0][2])
  expect(ui.action(retryLabel)).toBeNull()
})

it('preserves the failed upload and its retry action while a run is active', async () => {
  const ui = mountComposer()
  ui.upload.mockRejectedValueOnce(new Error('Response lost'))
  await ui.attach('retry.bin')
  ui.activeRun.value = true
  await nextTick()

  const retryLabel = 'Retry attachment upload retry.bin'
  ui.action(retryLabel)!.click()
  await nextTick()
  expect(ui.chip('retry.bin')?.textContent).toContain('Response lost')
  expect(ui.action(retryLabel)).not.toBeNull()
  expect(ui.upload).toHaveBeenCalledTimes(1)

  ui.activeRun.value = false
  await nextTick()
  ui.action(retryLabel)!.click()
  await vi.waitFor(() => expect(ui.contentParts.value).toHaveLength(1))
  expect(ui.upload.mock.lastCall?.[2]).toBe(ui.upload.mock.calls[0][2])
})

it('deletes only the removed candidate whose upload may have created a draft', async () => {
  const ui = mountComposer()
  await ui.attach('empty.bin', '')
  expect(ui.chip('empty.bin')?.textContent).toContain('Empty files cannot be attached')
  ui.action('Remove attachment empty.bin')!.click()
  await nextTick()
  expect(ui.chip('empty.bin')).toBeNull()
  expect(ui.upload).not.toHaveBeenCalled()
  expect(ui.remove).not.toHaveBeenCalled()

  ui.upload.mockRejectedValueOnce(new Error('Response lost'))
  await ui.attach('lost.bin')
  ui.action('Remove attachment lost.bin')!.click()
  await nextTick()
  expect(ui.chip('lost.bin')).toBeNull()
  expect(ui.remove.mock.calls).toEqual([[ui.upload.mock.calls[0][2]]])
})

it('retains the original receipt through scope adoption and keeps it after commit', async () => {
  const ui = mountComposer()
  await ui.attach('adopt.bin')
  const clientID = ui.upload.mock.calls[0][2]
  const nextScope = 'existing-conversation'
  expect(ui.chip('adopt.bin')?.dataset.agentkitAttachment).toBe(clientID)

  ui.composer.value!.adoptAttachmentDraftScope(initialScope, nextScope, ownerKey, [clientID])
  ui.scope.value = nextScope
  await nextTick()
  expect(ui.chip('adopt.bin')?.dataset.agentkitAttachment).toBe(clientID)
  expect(ui.upload).toHaveBeenCalledTimes(1)
  expect(ui.remove).not.toHaveBeenCalled()

  ui.composer.value!.commitAttachments([clientID], nextScope, ownerKey)
  ui.contentParts.value = []
  await nextTick()
  expect(ui.chip('adopt.bin')).toBeNull()
  expect(ui.remove).not.toHaveBeenCalled()
})
