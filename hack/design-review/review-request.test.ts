import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, onTestFinished } from 'vitest'
import ReviewRequest from '../../providers/app-studio/portal/src/ReviewRequest.vue'

const action = { runId: 'run-1', requestId: 'request-1', assistantMessageId: 'message-1' }
const approval = { interruptId: 'one', kind: 'permission', status: 'pending', action }

it('associates each clarification with its controls', () => {
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ReviewRequest, { interrupt: { ...approval, kind: 'follow_up' }, questions: [
    { id: 'a', question: 'Which report?', options: [{ label: 'Daily' }] },
    { id: 'b', question: 'Which format?', options: [{ label: 'CSV' }] },
  ] })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const groups = host.querySelectorAll('fieldset')
  expect([...groups].map(group => group.querySelector('legend')?.textContent)).toEqual(['Which report?', 'Which format?'])
  for (const [index, group] of [...groups].entries()) {
    expect(group.querySelector('button')).toBeTruthy()
    expect(group.querySelector('input')?.getAttribute('aria-label')).toBe(['Which report? Other answer', 'Which format? Other answer'][index])
  }
})

it.each(['allow', 'deny'] as const)('keeps the %s action focused and prevents repeated decisions during a failed request', async decision => {
  const host = document.createElement('div')
  document.body.append(host)
  const state = reactive<{ busy?: 'allow' | 'deny', error: string, decisions: string[] }>({ error: '', decisions: [] })
  const app = createApp({ render: () => h(ReviewRequest, { interrupt: approval, busy: state.busy, error: state.error, onDecide: (_request: unknown, value: 'allow' | 'deny') => { state.decisions.push(value); state.busy = value } }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const button = [...host.querySelectorAll('button')].find(el => el.textContent?.trim() === (decision === 'allow' ? 'Allow' : 'Deny'))!
  button.focus()
  button.click()
  await nextTick()
  expect(document.activeElement).toBe(button)
  expect(button.disabled).toBe(false)
  expect(button.getAttribute('aria-disabled')).toBe('true')
  const opposite = [...host.querySelectorAll('button')].find(el => el !== button)!
  expect(opposite.disabled).toBe(true)
  button.click()
  expect(state.decisions).toEqual([decision])
  state.busy = undefined
  state.error = 'Could not submit. Try again.'
  await nextTick()
  expect(document.activeElement).toBe(button)
  expect(host.querySelector('[role=alert]')?.textContent).toContain('Could not submit')
  button.click()
  expect(state.decisions).toEqual([decision, decision])
})

it.each(['Allow', 'Deny'])('keeps %s focused when an approval has a generic busy state', async label => {
  const host = document.createElement('div')
  document.body.append(host)
  const state = reactive({ busy: false, decisions: 0 })
  const app = createApp({ render: () => h(ReviewRequest, { interrupt: approval, busy: state.busy, onDecide: () => { state.decisions++; state.busy = true } }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const buttons = [...host.querySelectorAll('button')]
  const button = buttons.find(el => el.textContent?.trim() === label)!
  button.focus()
  button.click()
  await nextTick()
  expect(document.activeElement).toBe(button)
  for (const action of buttons) {
    expect(action.disabled).toBe(false)
    expect(action.getAttribute('aria-disabled')).toBe('true')
    action.click()
  }
  expect(state.decisions).toBe(1)
})

it('keeps invalid approval disabled while allowing denial', () => {
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(ReviewRequest, { interrupt: { ...approval, execDisclosureInvalid: true } })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const buttons = [...host.querySelectorAll('button')]
  expect(buttons.find(button => button.textContent?.trim() === 'Allow')?.disabled).toBe(true)
  expect(buttons.find(button => button.textContent?.trim() === 'Deny')?.disabled).toBe(false)
})

it('keeps Continue focused while blocking repeated clarification submissions', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const state = reactive({ busy: false, submissions: 0 })
  const app = createApp({ render: () => h(ReviewRequest, { interrupt: { ...approval, kind: 'follow_up' }, busy: state.busy, onSubmit: () => { state.submissions++; state.busy = true } }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const button = host.querySelector('button')!
  button.focus()
  button.click()
  await nextTick()
  expect(document.activeElement).toBe(button)
  expect(button.getAttribute('aria-disabled')).toBe('true')
  button.click()
  expect(state.submissions).toBe(1)
})
