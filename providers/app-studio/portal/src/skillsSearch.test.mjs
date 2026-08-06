import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

const skills = [
  { id: 'system:image-gen', packageName: 'image-gen', name: 'Image Gen', description: 'Generate or edit images', scope: 'system' },
  { id: 'project:review-agent', packageName: 'review-agent', name: 'Review Agent', description: 'Find actionable bugs', scope: 'project' },
  { id: 'system:openai-docs', packageName: 'openai-docs', name: 'OpenAI Docs', description: 'Reference product documentation', scope: 'system' },
]

test('filters the visible skill catalog by name, description, id, package, and scope', async () => {
  const { filterAssistantSkills } = await vite.ssrLoadModule('/src/skillsSearch.ts')

  assert.deepEqual(filterAssistantSkills(skills, 'image').map(({ id }) => id), ['system:image-gen'])
  assert.deepEqual(filterAssistantSkills(skills, 'ACTIONABLE bugs').map(({ id }) => id), ['project:review-agent'])
  assert.deepEqual(filterAssistantSkills(skills, 'openai-docs').map(({ id }) => id), ['system:openai-docs'])
  assert.deepEqual(filterAssistantSkills(skills, 'system docs').map(({ id }) => id), ['system:openai-docs'])
  assert.deepEqual(filterAssistantSkills(skills, 'missing'), [])
  assert.equal(filterAssistantSkills(skills, '   ').length, skills.length)
})
