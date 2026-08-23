import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

test('defaults and persists the Projects layout through the shared browser preference contract', () => {
  assert.match(app, /import LayoutSelector from '\.\/portalkit\/LayoutSelector\.vue'/)
  assert.match(app, /import \{ readLayoutPreference, writeLayoutPreference, type LayoutMode \} from '\.\/portalkit\/layoutPreference'/)
  assert.match(app, /const PROJECTS_LAYOUT_PREFERENCE_KEY = 'faros:portal:app-studio:projects-layout'/)
  assert.match(app, /const projectLayout = ref<LayoutMode>\(readLayoutPreference\(PROJECTS_LAYOUT_PREFERENCE_KEY\)\)/)
  assert.match(app, /watch\(projectLayout, mode => writeLayoutPreference\(PROJECTS_LAYOUT_PREFERENCE_KEY, mode\)\)/)
  assert.match(app, /<LayoutSelector v-model="projectLayout"[^>]*aria-label="Project layout"/)
})

test('keeps the existing gallery as the grid branch and uses table loading geometry in list mode', () => {
  const branchStart = app.indexOf('<template v-if="projectLayout === \'grid\'">')
  const listStart = app.indexOf('<ResourceTable', branchStart)
  assert.ok(branchStart >= 0 && listStart > branchStart)

  const grid = app.slice(branchStart, listStart)
  assert.match(grid, /v-if="\(loading \|\| !projectsLoaded\) && projects\.length === 0"/)
  assert.match(grid, /v-for="project in filteredProjects"/)
  assert.match(grid, /v-if="projectThumbnailURLs\[project\.name\]"/)
  assert.match(grid, /@click="enterProject\(project\)"/)
  assert.match(grid, /@click\.stop="requestDeleteProject\(project\)"/)
  assert.match(grid, /No projects available\./)
  assert.match(grid, /Preparing new project\.\.\./)
  assert.match(grid, /No projects match this search\./)

  const list = app.slice(listStart, app.indexOf('</ResourceTable>', listStart))
  assert.match(list, /:loaded="projectsLoaded"/)
  assert.match(list, /:loading="loading"/)
  assert.doesNotMatch(list, /shimmer/)
})

test('adapts filtered projects into an interactive canonical list with isolated delete actions', () => {
  assert.match(app, /import ResourceTable from '\.\/portalkit\/ResourceTable\.vue'/)
  assert.match(app, /import ResourceTableDeleteButton from '\.\/portalkit\/ResourceTableDeleteButton\.vue'/)
  assert.match(app, /const projectTableRows = computed<Array<Record<string, unknown>>>\(\(\) => filteredProjects\.value\.map/)
  assert.match(app, /name: project\.name,[\s\S]*description: project\.description \|\| project\.name,[\s\S]*phase: project\.phase \|\| 'Pending',[\s\S]*updated: projectTimestamp\(project\),[\s\S]*_project: project/)
  assert.match(app, /function projectFromTableRow\(row: Record<string, unknown>\): Project \| null/)

  const listStart = app.indexOf('<ResourceTable', app.indexOf('<template v-if="projectLayout === \'grid\'">'))
  const list = app.slice(listStart, app.indexOf('</ResourceTable>', listStart))
  assert.match(list, /:columns="projectTableColumns"/)
  assert.match(list, /:rows="projectTableRows"/)
  assert.match(list, /row-key="name"/)
  assert.match(list, /:row-aria-label="\(row\) => `Open project/)
  assert.match(list, /@row-click="enterProjectTableRow"/)
  assert.match(list, /<StatusBadge :status="String\(value\)"/)
  assert.match(list, /<ResourceTableDeleteButton[\s\S]*:busy-label="`Deleting project[\s\S]*:busy="deletingProjectName === String\(row\.name\)"[\s\S]*:disabled="busy"[\s\S]*@click="requestDeleteProjectTableRow\(row\)"/)
  assert.match(app, /const deletingProjectName = ref\(''\)/)
  assert.match(app, /deletingProjectName\.value = name/)
  assert.match(app, /deletingProjectName\.value = ''/)
  assert.doesNotMatch(list, /:busy="deletingProject"/)
})
