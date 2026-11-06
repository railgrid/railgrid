import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import AIConversationComposer from '../agentkit/AIConversationComposer.vue'
import { mountVue, settleVue, unmountVueApps } from './vue-helper'

afterEach(unmountVueApps)

async function composer() {
  return mountVue(AIConversationComposer, { modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
    attachmentTransport: { upload: vi.fn(async () => { throw new Error('Workspace upload limit exceeded') }), delete: vi.fn(async () => {}) } })
}
async function attach(view: Awaited<ReturnType<typeof composer>>, files: File[]) {
  const input = view.element.querySelector<HTMLInputElement>('input[type="file"]')!
  Object.defineProperty(input, 'files', { configurable: true, value: files })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await settleVue(12)
}

function receipt(id: string, filename = 'same.txt', sizeBytes = 10) {
  return { id, filename, contentType: 'text/plain', sizeBytes, sha256: 'a'.repeat(64), createdAt: '2026-11-05T00:00:00Z' }
}

describe('shared attachment error recovery', () => {
  it('keeps an ambiguously uploaded draft through retry without racing a deferred delete', async () => {
    let releaseDelete!: () => void
    const deleteGate = new Promise<void>(resolve => { releaseDelete = resolve })
    let delayedDelete: Promise<void> | undefined
    let uploadCount = 0
    const drafts = new Map<string, ReturnType<typeof receipt>>()
    const upload = vi.fn(async (_file: File, _signal: AbortSignal, clientID: string) => {
      uploadCount += 1
      if (uploadCount === 1) {
        const saved = receipt(clientID)
        drafts.set(clientID, saved)
        throw new Error('Upload response was lost')
      }
      const saved = drafts.get(clientID)
      if (!saved) throw new Error('The saved draft was deleted before retry')
      return saved
    })
    const removeDraft = vi.fn((clientID: string) => {
      delayedDelete = deleteGate.then(() => { drafts.delete(clientID) })
      return delayedDelete
    })
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })

    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    const clientID = upload.mock.calls[0][2]
    view.element.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload same.txt"]')!.click()
    await settleVue(12)

    expect(upload).toHaveBeenCalledTimes(2)
    expect(upload.mock.calls[1][2]).toBe(clientID)
    expect(view.element.querySelector(`[data-agentkit-attachment="${clientID}"]`)?.textContent).toContain('Ready')
    expect(removeDraft).not.toHaveBeenCalled()

    // A late cleanup would remove the server's idempotent retry receipt while
    // the composer still displays Ready. No cleanup should have been started.
    releaseDelete()
    await delayedDelete
    expect(drafts.has(clientID)).toBe(true)
    expect(view.element.querySelector(`[data-agentkit-attachment="${clientID}"]`)?.textContent).toContain('Ready')
  })

  it('cleans an ambiguous upload draft when the failed candidate is explicitly removed', async () => {
    let storedID = ''
    const drafts = new Set<string>()
    const upload = vi.fn(async (_file: File, _signal: AbortSignal, clientID: string) => {
      storedID = clientID
      drafts.add(clientID)
      throw new Error('Upload response was lost')
    })
    const removeDraft = vi.fn(async (clientID: string) => { drafts.delete(clientID) })
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })

    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment same.txt"]')!.click()
    await settleVue(8)

    expect(removeDraft).toHaveBeenCalledWith(storedID)
    expect(drafts.has(storedID)).toBe(false)
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('keeps an upload retry unchanged while blocked and still lets the user discard it', async () => {
    const upload = vi.fn(async () => { throw new Error('Upload response was lost') })
    const removeDraft = vi.fn(async () => {})
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    const chip = view.element.querySelector<HTMLElement>('[data-agentkit-attachment]')!
    const clientID = chip.dataset.agentkitAttachment!

    await view.setProps({ activeRun: true })
    chip.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload same.txt"]')!.click()
    await settleVue(8)

    expect(upload).toHaveBeenCalledTimes(1)
    expect(view.element.querySelectorAll('[data-agentkit-attachment]')).toHaveLength(1)
    expect(chip.textContent).toContain('Upload response was lost')
    expect(chip.querySelector('button[aria-label="Retry attachment upload same.txt"]')).toBeTruthy()

    chip.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment same.txt"]')!.click()
    await settleVue(8)
    expect(removeDraft).toHaveBeenCalledWith(clientID)
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('shows a retry validation error on the same chip and retries after a slot opens', async () => {
    let retryAttempts = 0
    const upload = vi.fn(async (file: File, _signal: AbortSignal, clientID: string) => {
      if (file.name === 'retry.txt' && ++retryAttempts === 1) throw new Error('Upload response was lost')
      return receipt(clientID, file.name, file.size)
    })
    const removeDraft = vi.fn(async () => {})
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [new File(['retry file'], 'retry.txt', { type: 'text/plain' })])
    const failedChip = view.element.querySelector<HTMLElement>('[data-agentkit-attachment]')!
    const clientID = failedChip.dataset.agentkitAttachment!

    for (let index = 0; index < 8; index++) {
      await attach(view, [new File([`other-${index}`], `other-${index}.txt`, { type: 'text/plain' })])
    }
    expect(upload).toHaveBeenCalledTimes(9)

    failedChip.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload retry.txt"]')!.click()
    await settleVue(8)

    expect(upload).toHaveBeenCalledTimes(9)
    expect(view.element.querySelectorAll('[data-agentkit-attachment]')).toHaveLength(9)
    expect(failedChip.textContent).toContain('A turn can contain at most 8 attachments.')
    expect(failedChip.querySelector('button[aria-label="Retry attachment upload retry.txt"]')).toBeTruthy()
    expect(failedChip.textContent).not.toContain('Uploading')

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment other-0.txt"]')!.click()
    await settleVue(8)
    failedChip.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload retry.txt"]')!.click()
    await settleVue(12)

    expect(upload).toHaveBeenCalledTimes(10)
    expect(upload.mock.calls[9][2]).toBe(clientID)
    expect(failedChip.getAttribute('title')).toBe('Ready')
    expect(view.element.querySelectorAll('[data-agentkit-attachment]')).toHaveLength(8)
  })

  it('keeps an ambiguous failed chip retryable and removable when uploads become unavailable', async () => {
    const upload = vi.fn(async () => { throw new Error('Upload response was lost') })
    const removeDraft = vi.fn(async () => {})
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    const chip = view.element.querySelector<HTMLElement>('[data-agentkit-attachment]')!
    const clientID = chip.dataset.agentkitAttachment!

    await view.setProps({ attachmentTransport: null })
    chip.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload same.txt"]')!.click()
    await settleVue(8)

    expect(upload).toHaveBeenCalledTimes(1)
    expect(view.element.querySelectorAll('[data-agentkit-attachment]')).toHaveLength(1)
    expect(chip.textContent).toContain('Attachments are unavailable for this conversation.')
    expect(chip.querySelector('button[aria-label="Retry attachment upload same.txt"]')).toBeTruthy()

    chip.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment same.txt"]')!.click()
    await settleVue(8)
    expect(removeDraft).toHaveBeenCalledWith(clientID)
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('cleans an ambiguous draft when a later retry is rejected by the attachment limit', async () => {
    let retryAttempts = 0
    const upload = vi.fn(async (file: File, _signal: AbortSignal, clientID: string) => {
      if (file.name === 'retry.txt' && ++retryAttempts === 1) throw new Error('Upload response was lost')
      return receipt(clientID, file.name, file.size)
    })
    const deleted: string[] = []
    const removeDraft = vi.fn(async (id: string) => { deleted.push(id) })
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [new File(['retry file'], 'retry.txt', { type: 'text/plain' })])
    const failedChip = view.element.querySelector<HTMLElement>('[data-agentkit-attachment]')!
    const clientID = failedChip.dataset.agentkitAttachment!
    for (let index = 0; index < 8; index++) {
      await attach(view, [new File([`other-${index}`], `other-${index}.txt`, { type: 'text/plain' })])
    }

    failedChip.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload retry.txt"]')!.click()
    await settleVue(8)
    expect(upload).toHaveBeenCalledTimes(9)
    expect(failedChip.textContent).toContain('A turn can contain at most 8 attachments.')

    failedChip.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment retry.txt"]')!.click()
    await settleVue(8)

    expect(deleted).toContain(clientID)
    expect(view.element.querySelector(`[data-agentkit-attachment="${clientID}"]`)).toBeNull()
  })

  it.each([
    { name: 'client validation errors', ownerKey: 'agent', file: () => new File([], 'empty.txt', { type: 'text/plain' }) },
    { name: 'missing owners', ownerKey: '', file: () => new File(['same bytes'], 'same.txt', { type: 'text/plain' }) },
  ])('removes $name locally without sending a server DELETE', async ({ ownerKey, file }) => {
    const upload = vi.fn(async () => receipt('unexpected'))
    const removeDraft = vi.fn(async () => {})
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: ownerKey,
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [file()])
    const chip = view.element.querySelector<HTMLElement>('[data-agentkit-attachment]')!
    expect(chip.textContent).toMatch(ownerKey ? /Empty files cannot be attached/ : /Attachments are unavailable/)

    chip.querySelector<HTMLButtonElement>('button[aria-label^="Remove attachment"]')!.click()
    await settleVue(8)

    expect(upload).not.toHaveBeenCalled()
    expect(removeDraft).not.toHaveBeenCalled()
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('does not DELETE an unuploaded local validation chip during scope cleanup', async () => {
    const upload = vi.fn(async () => receipt('unexpected'))
    const removeDraft = vi.fn(async () => {})
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [new File([], 'empty.txt', { type: 'text/plain' })])

    await view.setProps({ draftScopeKey: 'next-draft' })
    await settleVue(8)

    expect(upload).not.toHaveBeenCalled()
    expect(removeDraft).not.toHaveBeenCalled()
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('deletes a late receipt when an in-flight removal completes after the scope changes', async () => {
    let finishUpload!: () => void
    const upload = vi.fn((file: File, _signal: AbortSignal, clientID: string) => new Promise<unknown>(resolve => {
      finishUpload = () => resolve(receipt(`server-${clientID}`, file.name, file.size))
    }))
    const deleted: string[] = []
    const removeDraft = vi.fn(async (id: string) => { deleted.push(id) })
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })
    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    const clientID = upload.mock.calls[0][2]

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment same.txt"]')!.click()
    await settleVue(4)
    await view.setProps({ draftScopeKey: 'next-draft' })
    finishUpload()
    await settleVue(12)

    expect(deleted).toContain(clientID)
    expect(deleted).toContain(`server-${clientID}`)
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('cleans an unresolved upload draft when its draft scope changes', async () => {
    let storedID = ''
    const drafts = new Set<string>()
    const upload = vi.fn(async (_file: File, _signal: AbortSignal, clientID: string) => {
      storedID = clientID
      drafts.add(clientID)
      throw new Error('Upload response was lost')
    })
    const removeDraft = vi.fn(async (clientID: string) => { drafts.delete(clientID) })
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })

    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    await view.setProps({ draftScopeKey: 'next-draft' })

    expect(removeDraft).toHaveBeenCalledWith(storedID)
    expect(drafts.has(storedID)).toBe(false)
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
  })

  it('retries cleanup when an abandoned upload settles after scope cleanup', async () => {
    let failUpload!: (error: Error) => void
    const drafts = new Set<string>()
    const upload = vi.fn((_file: File, _signal: AbortSignal, clientID: string) => new Promise<unknown>((_resolve, reject) => {
      failUpload = error => {
        // Model a transport that ignores abort and persists after the scope
        // cleanup's first DELETE has already completed.
        drafts.add(clientID)
        reject(error)
      }
    }))
    const removeDraft = vi.fn(async (clientID: string) => { drafts.delete(clientID) })
    const view = await mountVue(AIConversationComposer, {
      modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      attachmentTransport: { upload, delete: removeDraft },
    })

    await attach(view, [new File(['same bytes'], 'same.txt', { type: 'text/plain' })])
    const clientID = upload.mock.calls[0][2]
    await view.setProps({ draftScopeKey: 'next-draft' })
    expect(removeDraft).toHaveBeenCalledTimes(1)
    expect(drafts.has(clientID)).toBe(false)

    failUpload(new Error('Upload settled after the draft changed'))
    await settleVue(8)

    expect(removeDraft).toHaveBeenCalledTimes(2)
    expect(removeDraft).toHaveBeenLastCalledWith(clientID)
    expect(drafts.has(clientID)).toBe(false)
  })

  it('renders upload errors visibly and describes image and text attachment groups', async () => {
    const view = await composer()
    await attach(view, [new File(['sample'], 'sample.txt', { type: 'text/plain' }), new File(['image'], 'sample.png', { type: 'image/png' })])
    const errors = view.element.querySelectorAll<HTMLElement>('.k-agentkit-attachment-error[role="status"]')
    expect(errors).toHaveLength(2)
    for (const error of errors) {
      expect(error.textContent).toBe('Workspace upload limit exceeded')
      expect(view.element.querySelector(`[aria-describedby="${error.id}"]`)?.getAttribute('role')).toBe('group')
    }
  })

  it('returns keyboard focus to the composer after removing a failed upload', async () => {
    const view = await composer()
    await attach(view, [new File(['sample'], 'sample.txt', { type: 'text/plain' })])
    const remove = view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment sample.txt"]')!
    remove.focus()
    remove.click()
    await settleVue(8)
    expect(view.element.querySelector('[data-agentkit-attachment]')).toBeNull()
    expect(document.activeElement).toBe(view.element.querySelector('.assistant-rich-composer'))
  })

  it('does not steal focus moved during the removal render flush', async () => {
    const view = await composer()
    await attach(view, [new File(['sample'], 'sample.txt', { type: 'text/plain' })])
    const other = document.createElement('button')
    document.body.append(other)
    const remove = view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment sample.txt"]')!
    remove.focus()
    remove.click()
    await nextTick(() => other.focus())
    await settleVue(8)
    expect(document.activeElement).toBe(other)
    other.remove()
  })

  it('returns focus to the composer when retry removes the focused action', async () => {
    const view = await composer()
    await attach(view, [new File(['sample'], 'sample.txt', { type: 'text/plain' })])
    const retry = view.element.querySelector<HTMLButtonElement>('button[aria-label="Retry attachment upload sample.txt"]')!
    retry.focus()
    retry.click()
    await settleVue(12)
    expect(document.activeElement).toBe(view.element.querySelector('.assistant-rich-composer'))
  })

  it('recovers focus during receipt deletion and leaves subsequent user focus alone', async () => {
    let finish!: () => void
    const deleted = new Promise<void>(resolve => { finish = resolve })
    const attachment = { id: 'stored', filename: 'stored.txt', contentType: 'text/plain', sizeBytes: 6, sha256: 'a'.repeat(64), createdAt: '2026-11-05T00:00:00Z' }
    const view = await mountVue(AIConversationComposer, { modelValue: '', skills: [], draftScopeKey: 'draft', attachmentOwnerKey: 'agent',
      contentParts: [{ type: 'attachment', attachment }], attachmentTransport: { upload: vi.fn(), delete: vi.fn(() => deleted) } })
    const remove = view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment stored.txt"]')!
    remove.focus()
    remove.click()
    await settleVue(4)
    expect(document.activeElement).toBe(view.element.querySelector('.assistant-rich-composer'))
    const other = document.createElement('button')
    document.body.append(other)
    other.focus()
    finish()
    await settleVue(8)
    expect(document.activeElement).toBe(other)
    other.remove()
  })

  it('does not move focus when attachment removal was invoked without owning focus', async () => {
    const view = await composer()
    await attach(view, [new File(['sample'], 'sample.txt', { type: 'text/plain' })])
    const other = document.createElement('button')
    document.body.append(other)
    other.focus()
    view.element.querySelector<HTMLButtonElement>('button[aria-label="Remove attachment sample.txt"]')!.click()
    await settleVue(8)
    expect(document.activeElement).toBe(other)
    other.remove()
  })
})
