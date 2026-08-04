import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./assistantPlan.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const {
  activeAssistantPlanMessage,
  assistantPlanProgress,
  assistantPlanStepStatusLabel,
  assistantPlanSummary,
  assistantPlanTerminalSummary,
  parseAssistantPlan,
} = await import(moduleURL)

test('parses an ordered three-step assistant plan', () => {
  assert.deepEqual(
    parseAssistantPlan({
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', activeForm: 'Updating the quote form', status: 'in_progress' },
        { content: 'Verify the preview', status: 'pending' },
      ],
    }),
    {
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', activeForm: 'Updating the quote form', status: 'in_progress' },
        { content: 'Verify the preview', status: 'pending' },
      ],
    },
  )
})

test('rejects plans with zero or more than fifty steps', () => {
  assert.equal(parseAssistantPlan({ steps: [] }), undefined)
  assert.equal(
    parseAssistantPlan({
      steps: Array.from({ length: 51 }, () => ({ content: 'Inspect project', status: 'pending' })),
    }),
    undefined,
  )
})

test('rejects blank content and unsupported statuses', () => {
  assert.equal(parseAssistantPlan({ steps: [{ content: '  ', status: 'pending' }] }), undefined)
  assert.equal(parseAssistantPlan({ steps: [{ content: 'Inspect project', status: 'blocked' }] }), undefined)
})

test('rejects labels longer than one hundred twenty UTF-8 bytes', () => {
  assert.equal(
    parseAssistantPlan({ steps: [{ content: 'x'.repeat(121), status: 'pending' }] }),
    undefined,
  )
  assert.equal(
    parseAssistantPlan({ steps: [{ content: '😀'.repeat(31), status: 'pending' }] }),
    undefined,
  )
})

test('rejects a non-string active form', () => {
  assert.equal(
    parseAssistantPlan({ steps: [{ content: 'Inspect project', activeForm: 42, status: 'pending' }] }),
    undefined,
  )
})

test('strips arbitrary metadata outside the documented plan fields', () => {
  assert.deepEqual(
    parseAssistantPlan({
      steps: [{ content: 'Inspect project', activeForm: 'Inspecting project', status: 'pending', secret: 'discard' }],
      internal: { debug: true },
    }),
    {
      steps: [{ content: 'Inspect project', activeForm: 'Inspecting project', status: 'pending' }],
    },
  )
})

test('derives compact progress using active form before content', () => {
  const plan = {
    steps: [
      { content: 'Inspect the quote form', status: 'completed' },
      { content: 'Update the quote form', activeForm: 'Updating the quote form', status: 'in_progress' },
      { content: 'Verify the preview', status: 'pending' },
    ],
  }

  assert.deepEqual(assistantPlanProgress(plan), {
    completed: 1,
    total: 3,
    activeLabel: 'Updating the quote form',
  })
  assert.equal(
    assistantPlanProgress({ steps: [{ content: 'Verify the preview', status: 'in_progress' }] }).activeLabel,
    'Verify the preview',
  )
})

test('summarizes progress with the active form', () => {
  assert.equal(
    assistantPlanSummary({
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', activeForm: 'Updating the quote form', status: 'in_progress' },
        { content: 'Verify the preview', status: 'pending' },
      ],
    }),
    'Building · 1 of 3 steps · Updating the quote form',
  )
})

test('summarizes completed progress without a trailing separator', () => {
  assert.equal(
    assistantPlanSummary({
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', status: 'completed' },
        { content: 'Verify the preview', status: 'completed' },
      ],
    }),
    '3 of 3 steps',
  )
})

test('summarizes terminal plans from the persisted snapshot for complete and partial runs', () => {
  assert.equal(
    assistantPlanTerminalSummary({
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', status: 'completed' },
      ],
    }, 'completed'),
    'Plan completed · 2 of 2 steps completed',
  )
  assert.equal(
    assistantPlanTerminalSummary({
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Update the quote form', status: 'pending' },
      ],
    }, 'failed'),
    'Plan ended · 1 of 2 steps completed',
  )
})

test('labels every plan step status for nonvisual presentation', () => {
  assert.equal(assistantPlanStepStatusLabel('completed'), 'Completed')
  assert.equal(assistantPlanStepStatusLabel('in_progress'), 'In progress')
  assert.equal(assistantPlanStepStatusLabel('pending'), 'Pending')
})

test('selects only the streaming active assistant message plan', () => {
  const oldPlan = {
    steps: [{ content: 'Inspect the quote form', status: 'completed' }],
  }
  const activePlan = {
    steps: [{ content: 'Update the quote form', status: 'in_progress' }],
  }
  const messages = [
    { id: 'assistant-old', role: 'assistant', plan: oldPlan },
    { id: 'assistant-active', role: 'assistant', plan: activePlan },
  ]

  assert.equal(activeAssistantPlanMessage(messages, 'assistant-active', true, false)?.id, 'assistant-active')
  assert.equal(activeAssistantPlanMessage(messages, 'assistant-active', false, false), undefined)
  assert.equal(activeAssistantPlanMessage(messages, 'missing', true, false), undefined)
  assert.equal(
    activeAssistantPlanMessage(
      [{ id: 'assistant-active', role: 'assistant' }],
      'assistant-active',
      true,
      false,
    ),
    undefined,
  )
})

test('does not select the prior terminal run plan while a new run start is pending', () => {
  const previousPlan = {
    steps: [{ content: 'Verify the previous change', status: 'completed' }],
  }
  const messages = [
    { id: 'assistant-previous', role: 'assistant', plan: previousPlan },
  ]

  assert.equal(
    activeAssistantPlanMessage(messages, 'assistant-previous', true, true),
    undefined,
  )
})
