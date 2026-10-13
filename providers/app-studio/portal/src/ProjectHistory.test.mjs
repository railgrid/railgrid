import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const history = await readFile(new URL('./ProjectHistory.vue', import.meta.url), 'utf8')

test('renders a source-only Git history restore experience', () => {
  assert.match(history, /AIWorkbenchPane title="History"/)
  assert.match(history, /Git history and production stay unchanged/)
  assert.match(history, /role="radiogroup"[\s\S]*aria-label="Project commits"/)
  assert.match(history, /repositoryCommitSelectable\(commit\)/)
  assert.match(history, /Restore project files/)
  assert.doesNotMatch(history, /Load from Git|loadFromGit|hydrate/i)
  assert.match(history, /Uncommitted changes and files added after this commit will be removed/)
  assert.doesNotMatch(history, /Current production|releaseID|deployable release|production images|promote/i)
})

test('keeps commit navigation outside the selectable radio row', () => {
  const radioStart = history.indexOf('role="radio"')
  const radioEnd = history.indexOf('</button>', radioStart)
  const commitLink = history.indexOf('>View commit</a>', radioEnd)
  assert.ok(radioStart >= 0 && radioEnd > radioStart && commitLink > radioEnd)
  assert.doesNotMatch(history.slice(radioStart, radioEnd), /<a\b/)
})

import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

test('renders distinct initial loading, empty, and failed history states', async () => {
  const vite = await createServer({ configFile: false, plugins: [vue()], server: { middlewareMode: true, hmr: false } })
  try {
    const { default: History } = await vite.ssrLoadModule('/src/ProjectHistory.vue')
    const render = (props = {}) => renderToString(createSSRApp(History, {
      repositoryRef: 'repo', repositoryStatus: 'Ready', commits: [], selectedCommit: '', ...props,
    }))
    const revision = { name: 'saved', phase: 'Succeeded', commitSHA: 'a'.repeat(40), message: 'Saved changes' }
    const unselected = await render({ commits: [revision] })
    assert.match(unselected, /aria-checked="false"/)
    assert.match(unselected, /tabindex="0"/)
    assert.match(unselected, /Select a commit above to enable restore/)
    assert.match(unselected, /class="k-btn k-btn--danger ml-auto" disabled/)
    assert.match(unselected, /1 commit/)
    assert.doesNotMatch(unselected, /Scroll to see more commits/)

    const twoCommits = await render({ commits: [revision, { ...revision, name: 'older', commitSHA: 'b'.repeat(40) }] })
    assert.match(twoCommits, /2 commits/)
    assert.match(twoCommits, /Scroll to see more commits\. Use arrow keys to select\./)

    const disabledForSelection = await render({
      commits: [revision], restoreDisabled: true, restoreDisabledReason: 'Select a successful commit to restore.',
    })
    assert.match(disabledForSelection, /Select a successful commit to restore\./)
    assert.doesNotMatch(disabledForSelection, /Select a commit above to enable restore/)
    assert.equal((disabledForSelection.match(/role="status"/g) || []).length, 1)

    const disabledForRefresh = await render({
      commits: [revision], selectedCommit: revision.commitSHA, restoreDisabled: true,
      restoreDisabledReason: 'Wait for Git history to finish refreshing before restoring project files.',
    })
    assert.match(disabledForRefresh, /Wait for Git history to finish refreshing before restoring project files\./)

    const selected = await render({ commits: [revision], selectedCommit: revision.commitSHA })
    assert.match(selected, /aria-checked="true"/)
    assert.doesNotMatch(selected, /class="k-btn k-btn--danger ml-auto" disabled/)
    const loading = await render({ refreshing: true })
    assert.match(loading, /Loading saved commits/)
    assert.doesNotMatch(loading, /No saved commits|Selected commit|Restore project files/)
    const empty = await render()
    assert.match(empty, /No saved commits yet/)
    assert.doesNotMatch(empty, /Loading saved commits|Selected commit|Restore project files/)
    const failed = await render({ error: 'History could not be fetched', repositoryStatus: 'Pending' })
    assert.match(failed, /History could not be fetched/)
    assert.doesNotMatch(failed, /No saved commits|Git commit history is not ready/)
  } finally {
    await vite.close()
  }
})
