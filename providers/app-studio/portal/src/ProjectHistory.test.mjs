import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const history = await readFile(new URL('./ProjectHistory.vue', import.meta.url), 'utf8')

test('renders a source-only Git history restore experience', () => {
  assert.match(history, /Project file history/)
  assert.match(history, /Git history and production stay unchanged/)
  assert.match(history, /role="radiogroup"[\s\S]*aria-label="Project commits"/)
  assert.match(history, /repositoryCommitSelectable\(commit\)/)
  assert.match(history, /Restore project files/)
  assert.match(history, /does not move the Git branch, create a commit, or change production/)
  assert.doesNotMatch(history, /Current production|releaseID|deployable release|production images|promote/i)
})

test('keeps commit navigation outside the selectable radio row', () => {
  const radioStart = history.indexOf('role="radio"')
  const radioEnd = history.indexOf('</button>', radioStart)
  const commitLink = history.indexOf('>View commit</a>', radioEnd)
  assert.ok(radioStart >= 0 && radioEnd > radioStart && commitLink > radioEnd)
  assert.doesNotMatch(history.slice(radioStart, radioEnd), /<a\b/)
})
