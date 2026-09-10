import { describe, expect, it } from 'vitest'
import AIWorkspace from '../agentkit/AIWorkspace.vue'
import { mountVue, settleVue } from './vue-helper'

describe('AIWorkspace', () => {
  it('clamps a desktop split when an embedded container narrows', async () => {
    const mounted = await mountVue(AIWorkspace, {
      open: true,
      mobileBreakpoint: 700,
      minConversationWidth: 480,
      minWorkbenchWidth: 360,
    })
    const workspace = mounted.element.querySelector<HTMLElement>('[data-k-ai-workspace]')!
    const resizeHandle = mounted.element.querySelector<HTMLElement>('[data-k-ai-workspace-resize]')!
    let width = 1200
    Object.defineProperty(workspace, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ width, left: 0, top: 0, right: width, bottom: 0, height: 0 } as DOMRect),
    })

    window.dispatchEvent(new Event('resize'))
    await settleVue()
    resizeHandle.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true, cancelable: true }))
    await settleVue()
    expect(Number(resizeHandle.getAttribute('aria-valuenow'))).toBe(Number(resizeHandle.getAttribute('aria-valuemax')))

    width = 850
    window.dispatchEvent(new Event('resize'))
    await settleVue()
    const max = Number(resizeHandle.getAttribute('aria-valuemax'))
    const now = Number(resizeHandle.getAttribute('aria-valuenow'))
    expect(now).toBeLessThanOrEqual(max)
    expect(now).toBe(max)
  })
})
