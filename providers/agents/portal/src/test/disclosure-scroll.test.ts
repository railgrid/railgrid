import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import AIActionRow from '../agentkit/AIActionRow.vue'
import AITurnProgress from '../agentkit/AITurnProgress.vue'
import { useDisclosureScroll } from '../agentkit/useDisclosureScroll'
import { mountVue, settleVue } from './vue-helper'

let frames: Map<number, FrameRequestCallback>
let time: number
let nextFrame: number
let panelSequence = 0
beforeEach(() => {
  frames = new Map()
  time = 0
  nextFrame = 0
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.set(++nextFrame, callback); return nextFrame })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  vi.spyOn(performance, 'now').mockImplementation(() => time)
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
function frame() {
  time += 16
  const callbacks = [...frames.values()]
  frames.clear()
  callbacks.forEach(callback => callback(time))
}
function rect(top: number, bottom: number): DOMRect { return { top, bottom } as DOMRect }
async function setup(triggerTop = 300, panelBottom = 600) {
  const panelID = `scroll-test-panel-${++panelSequence}`
  const view = await mountVue(defineComponent({
    setup() {
      const compensate = useDisclosureScroll()
      return () => h('div', { class: 'k-ai-transcript-scroll' }, [
        h('section', { onClickCapture: compensate }, [
          h('button', { 'aria-controls': panelID, 'aria-expanded': 'false' }, 'Worked for'),
          h('div', { id: panelID }, 'Tool results'),
        ]),
      ])
    },
  }), {})
  const scroller = view.element.querySelector<HTMLElement>('.k-ai-transcript-scroll')!
  const button = scroller.querySelector('button')!
  const panel = scroller.querySelector<HTMLElement>(`#${panelID}`)!
  Object.defineProperty(scroller, 'clientHeight', { value: 400 })
  scroller.getBoundingClientRect = () => rect(0, 400)
  button.getBoundingClientRect = () => rect(triggerTop - scroller.scrollTop, triggerTop + 24 - scroller.scrollTop)
  panel.getBoundingClientRect = () => rect(triggerTop + 24 - scroller.scrollTop, panelBottom - scroller.scrollTop)
  async function expand() {
    button.click()
    button.setAttribute('aria-expanded', 'true')
    await settleVue()
    frame()
  }
  return { view, scroller, button, panel, expand }
}

describe('user disclosure scroll compensation', () => {
  it('reveals overflowing content with only the needed scroll and follows its animation', async () => {
    const { scroller, panel, expand } = await setup(300, 440)
    await expand()
    expect(scroller.scrollTop).toBe(48)
    panel.getBoundingClientRect = () => rect(324 - scroller.scrollTop, 600 - scroller.scrollTop)
    frame()
    expect(scroller.scrollTop).toBe(208)
    frame()
    expect(scroller.scrollTop).toBe(208)
  })
  it('keeps the trigger visible for output taller than the viewport', async () => {
    const { scroller, expand } = await setup(300, 1200)
    await expand()
    expect(scroller.scrollTop).toBe(292)
  })
  it('leaves content that already fits alone', async () => {
    const { scroller, expand } = await setup(100, 250)
    await expand()
    expect(scroller.scrollTop).toBe(0)
  })
  it('does not react to automatic expansion or offscreen triggers', async () => {
    const { scroller, button } = await setup()
    button.setAttribute('aria-expanded', 'true')
    await settleVue()
    frame()
    expect(scroller.scrollTop).toBe(0)
    const offscreen = await setup(500, 1000)
    await offscreen.expand()
    expect(offscreen.scroller.scrollTop).toBe(0)
  })
  it.each(['wheel', 'touchstart', 'pointerdown', 'keydown'])('yields to %s', async (type) => {
    const { scroller, button } = await setup()
    button.click()
    button.setAttribute('aria-expanded', 'true')
    await settleVue()
    scroller.dispatchEvent(type === 'keydown' ? new KeyboardEvent(type, { key: 'PageUp' }) : new Event(type))
    frame()
    expect(scroller.scrollTop).toBe(0)
    expect(frames.size).toBe(0)
  })
  it('coordinates a real tool disclosure nested inside Worked for with one scroll owner', async () => {
    const expanded = ref(false)
    const view = await mountVue(defineComponent({
      setup: () => () => h('div', { class: 'k-ai-transcript-scroll' }, [
        h(AITurnProgress, { expanded: true, status: 'completed' }, {
          details: () => h(AIActionRow, {
            title: 'Read files', expandable: true, expanded: expanded.value,
            detailsId: 'nested-tool-details', onToggle: () => { expanded.value = !expanded.value },
          }, { details: () => h('pre', 'file contents') }),
        }),
      ]),
    }), {})
    const scroller = view.element.querySelector<HTMLElement>('.k-ai-transcript-scroll')!
    const button = view.element.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!
    Object.defineProperty(scroller, 'clientHeight', { value: 400 })
    scroller.getBoundingClientRect = () => rect(0, 400)
    button.getBoundingClientRect = () => rect(300 - scroller.scrollTop, 324 - scroller.scrollTop)
    button.click()
    await settleVue()
    expect(button.getAttribute('aria-expanded')).toBe('true')
    const details = document.getElementById('nested-tool-details')!
    details.getBoundingClientRect = () => rect(324 - scroller.scrollTop, 1000 - scroller.scrollTop)
    const disclosure = details.closest<HTMLElement>('.k-ai-disclosure')!
    disclosure.getBoundingClientRect = () => rect(324 - scroller.scrollTop, 440 - scroller.scrollTop)
    // Vue's Transition also owns a double animation frame; let it settle.
    frame()
    frame()
    frame()
    expect(frames.size).toBe(1)
    expect(scroller.scrollTop).toBe(48)
    disclosure.getBoundingClientRect = () => rect(324 - scroller.scrollTop, 600 - scroller.scrollTop)
    frame()
    expect(scroller.scrollTop).toBe(208)
  })
  it('cancels on unmount, collapse, and after the disclosure transition', async () => {
    const { view, button, expand } = await setup()
    await expand()
    button.click() // collapse cancels the previous compensation
    expect(frames.size).toBe(0)
    button.setAttribute('aria-expanded', 'false')
    await expand()
    for (let i = 0; i < 20; i++) frame()
    expect(frames.size).toBe(0)
    button.setAttribute('aria-expanded', 'false')
    await expand()
    view.unmount()
    expect(frames.size).toBe(0)
  })
})
