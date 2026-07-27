import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./assistantProgress.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { parseAssistantTraceHeader, summarizeAssistantTrace } = await import(moduleURL)

test('separates a live A2UI tool-card header into product label and tool name', () => {
  assert.deepEqual(
    parseAssistantTraceHeader('Updated src/App.vue · write_file'),
    {
      label: 'Updated src/App.vue',
      tool: 'write_file',
    },
  )
  assert.deepEqual(
    parseAssistantTraceHeader('Checked development preview', 'verify_development_runtime'),
    {
      label: 'Checked development preview',
      tool: 'verify_development_runtime',
    },
  )
})

test('summarizes actions using safe product labels instead of tool names', () => {
  const summary = summarizeAssistantTrace([
    { label: 'Read 6 project files', tool: 'list_project_files' },
    { label: 'Updated src/App.vue', tool: 'write_file' },
    { label: 'Checked development preview', tool: 'verify_development_runtime' },
  ])

  assert.equal(
    summary,
    'Read 6 project files · Updated src/App.vue · Checked development preview',
  )
  assert.equal(summary.includes('write_file'), false)
})

test('bounds the collapsed summary while retaining the action count cue', () => {
  const summary = summarizeAssistantTrace([
    { label: `Updated ${'nested/'.repeat(20)}App.vue` },
    { label: 'Updated src/style.css' },
    { label: 'Checked development preview' },
    { label: 'Committed 2 files' },
  ])

  assert.match(summary, /^Updated .+\.\.\. · Updated src\/style\.css · Checked development preview · 1 more$/)
  assert.equal(summary.length < 220, true)
})

test('ignores empty labels', () => {
  assert.equal(
    summarizeAssistantTrace([
      { label: '  ' },
      { label: 'Updated src/App.vue' },
    ]),
    'Updated src/App.vue',
  )
})
