import type { QuestionReply } from '../types'
import { createApp, h, nextTick, ref } from 'vue'
import { expect, it } from 'vitest'
import AgentQuestionCard from '../components/AgentQuestionCard.vue'
import { questionPresentation } from '../question-presentation'

const prompt = 'Where should the guide go?\nOptions:\n- Line guide: Keep it with line setup.\n- README: Put it near the entry point.'

it('requires a send action for a choice and retains a custom answer across a failed send', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const busy = ref(false)
  const questionId = ref('question-1')
  const answers: QuestionReply[] = []
  const app = createApp(() => h(AgentQuestionCard, { questionId: questionId.value, prompt, busy: busy.value, onAnswer: value => answers.push(value) }))
  app.mount(host)
  try {
    const send = () => host.querySelector<HTMLButtonElement>('button[type="submit"]')!
    const input = host.querySelector<HTMLInputElement>('input[type="text"]')!
    expect(send().disabled).toBe(true)
    host.querySelector<HTMLInputElement>('input[type="radio"]')!.click()
    await nextTick()
    expect(answers).toEqual([])
    send().click()
    expect(answers).toEqual(['Line guide'])
    input.value = 'A different location'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(host.querySelector('input[type="radio"]:checked')).toBeNull()
    busy.value = true
    await nextTick()
    expect(send().disabled).toBe(true)
    busy.value = false
    await nextTick()
    expect(input.value).toBe('A different location')
    send().click()
    expect(answers).toEqual(['Line guide', 'A different location'])
    questionId.value = 'question-2'
    await nextTick()
    expect(send().disabled).toBe(true)
  } finally {
    app.unmount()
    host.remove()
  }
})

it('keeps multi-question and unstructured prompts intact', () => {
  const multiple = `${prompt}\n\nWhich heading should it use?`
  expect(questionPresentation(multiple)).toEqual({ prompt: multiple, options: [] })
  expect(questionPresentation('What should I do?')).toEqual({ prompt: 'What should I do?', options: [] })
})

it('submits a choice, write-in, and explicit skip as one group', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const answers: QuestionReply[] = []
  const app = createApp(() => h(AgentQuestionCard, {
    questionId: 'group',
    questions: [
      { id: 'location', prompt: 'Where should it go?', options: [{ label: 'README (Recommended)' }] },
      { id: 'name', prompt: 'What should it be called?' },
      { id: 'owner', prompt: 'Who should own it?' },
    ],
    onAnswer: value => answers.push(value),
  }))
  app.mount(host)
  try {
    const [location, name, owner] = host.querySelectorAll('fieldset')
    const send = host.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(send.disabled).toBe(true)
    location.querySelector<HTMLInputElement>('input[type="radio"]')!.click()
    const input = name.querySelector<HTMLInputElement>('input[type="text"]')!
    input.value = 'Operator guide'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(send.disabled).toBe(true)
    owner.querySelector<HTMLInputElement>('input[type="radio"]')!.click()
    await nextTick()
    expect(answers).toEqual([])
    send.click()
    expect(answers).toEqual([{ location: 'README (Recommended)', name: 'Operator guide', owner: null }])
  } finally {
    app.unmount()
    host.remove()
  }
})
