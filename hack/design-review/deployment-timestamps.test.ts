import { createApp, h, nextTick } from 'vue'
import { expect, it } from 'vitest'
import DeploymentExample from '../deployment-detail-visual/Example.vue'

it('uses complete, inspectable timestamps for the deployment summary, conditions, and events', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(DeploymentExample) })
  app.mount(root)
  const times = [...root.querySelectorAll<HTMLTimeElement>('time')]
  expect(times).toHaveLength(7)
  expect(times.every(time => Number.isFinite(Date.parse(time.dateTime)))).toBe(true)
  const events = [...root.querySelectorAll<HTMLTimeElement>('#events time')].map(time => Date.parse(time.dateTime))
  expect(events[0] - events[3]).toBe(12 * 60_000)
  expect(times[0].dateTime).toBe(times[2].dateTime)
  const eventTime = root.querySelector<HTMLButtonElement>('#events .k-resource-timestamp__button')!
  expect(eventTime.getAttribute('aria-label')).toContain('Readiness check timed out')
  eventTime.focus(); eventTime.click()
  await nextTick()
  expect(document.activeElement).toBe(eventTime)
  expect(eventTime.getAttribute('aria-pressed')).toBe('true')
  expect(eventTime.getAttribute('aria-label')).toMatch(/\d{4}/)
  app.unmount(); root.remove()
})
