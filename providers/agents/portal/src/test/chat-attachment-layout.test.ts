import { describe, expect, it } from 'vitest'
import ChatMessage from '../views/ChatMessage.vue'
import { mountVue } from './vue-helper'

const attachment = { id: 'image', filename: 'image.png', contentType: 'image/png', sizeBytes: 12, sha256: 'a'.repeat(64), createdAt: '2026-11-05T00:00:00Z' }

describe('sent attachment layout', () => {
  it.each(['what is this', '', ' '])('keeps attachments above and outside the text bubble (%s)', async content => {
    const view = await mountVue(ChatMessage, { message: { id: 'user-image', role: 'user', content, tools: [], contentParts: [{ type: 'attachment', attachment }] } })
    const receipts = view.element.querySelector('[aria-label="Attachments"]')!
    expect(receipts.closest('.k-ai-message__before')).not.toBeNull()
    expect(receipts.closest('.k-ai-message__content--bubble')).toBeNull()
    const bubble = view.element.querySelector('.k-ai-message__content--bubble')
    if (content.trim()) {
      expect(bubble?.textContent).toContain(content)
      expect(receipts.compareDocumentPosition(bubble!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    } else expect(bubble).toBeNull()
  })
})
