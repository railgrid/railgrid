import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'
import AIActionRow from '../../provider-sdk/agentkit-vue/AIActionRow.vue'

const mounts: { app: App, element: HTMLElement }[] = []
afterEach(() => {
  for (const { app, element } of mounts.splice(0)) { app.unmount(); element.remove() }
})
async function settleVue(passes = 4, delayMS = 0) {
  for (let i = 0; i < passes; i++) await nextTick()
  if (delayMS) await new Promise(resolve => setTimeout(resolve, delayMS))
}
async function mountVue(component: Parameters<typeof createApp>[0], props: Record<string, unknown>) {
  const element = document.createElement('div')
  document.body.append(element)
  const app = createApp(component, props)
  app.mount(element)
  mounts.push({ app, element })
  await settleVue()
  return { element }
}

describe('canonical activity row disclosure', () => {
  it('keeps distinct targets while lazily mounting controlled details', async () => {
    const open = ref(false)
    let renders = 0
    const view = await mountVue(defineComponent({
      setup: () => () => h('div', ['a', 'b'].map(id => h(AIActionRow, {
        title: `Action ${id}`, expandable: true, expanded: id === 'a' && open.value,
        detailsId: `details-${id}`, onToggle: () => { open.value = !open.value },
      }, { details: () => { renders++; return h('p', `Evidence ${id}`) } }))),
    }), {})
    const buttons = [...view.element.querySelectorAll<HTMLButtonElement>('button')]
    const target = view.element.querySelector('#details-a')!
    expect(buttons.map(button => button.getAttribute('aria-controls'))).toEqual(['details-a', 'details-b'])
    expect(view.element.querySelectorAll('#details-a')).toHaveLength(1)
    expect(view.element.querySelectorAll('#details-b')).toHaveLength(1)
    expect(buttons[0].getAttribute('aria-expanded')).toBe('false')
    expect(target.textContent).toBe('')
    expect(renders).toBe(0)

    buttons[0].click()
    await settleVue()
    expect(buttons[0].getAttribute('aria-expanded')).toBe('true')
    expect(view.element.querySelector('#details-a')).toBe(target)
    expect(target.textContent).toContain('Evidence a')
    expect(view.element.querySelector('#details-b')?.textContent).toBe('')

    buttons[0].click()
    await settleVue(4, 100)
    expect(buttons[0].getAttribute('aria-expanded')).toBe('false')
    expect(view.element.querySelector('#details-a')).toBe(target)
    expect(target.textContent).toBe('')
  })

  it('does not advertise a details target without a slot', async () => {
    const view = await mountVue(AIActionRow, { title: 'Action', expandable: true, detailsId: 'absent' })
    expect(view.element.querySelector('button')?.hasAttribute('aria-controls')).toBe(false)
    expect(view.element.querySelector('#absent')).toBeNull()
  })
})
