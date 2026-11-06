import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AIAttachmentReceipts from '../agentkit/AIAttachmentReceipts.vue'
import { mountVue, settleVue, text, unmountVueApps } from './vue-helper'

const originalCreateObjectURL = URL.createObjectURL
const originalRevokeObjectURL = URL.revokeObjectURL
const createObjectURL = vi.fn(() => 'blob:agentkit-preview')
const revokeObjectURL = vi.fn()

beforeEach(() => {
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })
})

function receipt(id: string, contentType: string, sizeBytes = 12) {
  return { id, filename: `${id}.${contentType.split('/')[1]}`, contentType, sizeBytes, sha256: 'a'.repeat(64), createdAt: '2026-11-05T00:00:00Z' }
}

afterEach(() => {
  unmountVueApps()
  vi.clearAllMocks()
})

describe('AgentKit historical attachment receipts', () => {
  it('loads previews only for supported, in-bound images and renders opaque files as metadata', async () => {
    const loadAttachment = vi.fn(async () => new Blob(['image'], { type: 'image/png' }))
    const view = await mountVue(AIAttachmentReceipts, {
      attachments: [
        receipt('supported', 'image/png'),
        receipt('svg', 'image/svg+xml'),
        receipt('gif', 'image/gif'),
        receipt('large-image', 'image/png', (8 << 20) + 1),
        receipt('large-text', 'text/plain', (1 << 20) + 1),
      ],
      loadAttachment,
    })
    await settleVue(8)

    expect(loadAttachment).toHaveBeenCalledOnce()
    expect(loadAttachment).toHaveBeenCalledWith(expect.objectContaining({ id: 'supported' }), expect.any(AbortSignal))
    expect(view.element.querySelectorAll('img')).toHaveLength(1)
    expect(view.element.querySelectorAll('[role="note"]')).toHaveLength(4)
    expect(text(view.element)).toContain('Stored file; contents unavailable for analysis')
    view.unmount()
  })

  it('offers a named retry after an authorized historical image preview read fails', async () => {
    let attempts = 0
    const loadAttachment = vi.fn(async () => {
      attempts += 1
      if (attempts === 1) throw new Error('Temporary read failure')
      return new Blob(['image'], { type: 'image/png' })
    })
    const view = await mountVue(AIAttachmentReceipts, {
      attachments: [receipt('screen', 'image/png')],
      loadAttachment,
    })
    await settleVue(8)

    const retry = view.element.querySelector<HTMLButtonElement>('button[aria-label="Retry image preview for screen.png"]')
    expect(retry).toBeTruthy()
    expect(text(view.element)).toContain('Temporary read failure')
    retry!.click()
    await settleVue(8)

    expect(loadAttachment).toHaveBeenCalledTimes(2)
    expect(view.element.querySelector('img')?.getAttribute('src')).toBe('blob:agentkit-preview')
    expect(view.element.querySelector('button[aria-label="Retry image preview for screen.png"]')).toBeNull()
    view.unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:agentkit-preview')
  })
})

afterEach(() => {
  if (originalCreateObjectURL) Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: originalCreateObjectURL })
  else Reflect.deleteProperty(URL, 'createObjectURL')
  if (originalRevokeObjectURL) Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: originalRevokeObjectURL })
  else Reflect.deleteProperty(URL, 'revokeObjectURL')
})
