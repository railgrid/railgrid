import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, onTestFinished } from 'vitest'
import AgentDigestSchedule from '../../providers/agents/portal/src/components/AgentDigestSchedule.vue'

it('moves focus between schedule editors without losing the expression', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const expression = ref('0 8 * * 1-5')
  const missing = ref(false)
  const app = createApp({ render: () => h(AgentDigestSchedule, {
    modelValue: expression.value, defaultValue: '0 8 * * 1-5', disabled: false,
    timeMissing: missing.value, 'onUpdate:modelValue': value => { expression.value = value },
    'onUpdate:timeMissing': value => { missing.value = value },
  }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const button = (name: string) => [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === name)!
  button('Edit cron').focus()
  button('Edit cron').click()
  await nextTick(); await nextTick()
  expect(document.activeElement).toBe(host.querySelector('#agent-create-param-digestTime'))
  expect((document.activeElement as HTMLInputElement).value).toBe('0 8 * * 1-5')
  button('Use time and weekdays').click()
  await nextTick(); await nextTick()
  expect(document.activeElement).toBe(host.querySelector('input[type="time"]'))
  expect(expression.value).toBe('0 8 * * 1-5')
})
