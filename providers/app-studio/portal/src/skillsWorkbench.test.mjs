import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
const { assistantSkillsRequestIsCurrent } = await vite.ssrLoadModule('/src/SkillsWorkbench.vue')
test.after(async () => vite.close())

const [app, api, workbench, skillCatalog] = await Promise.all([
  readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  readFile(new URL('./api.ts', import.meta.url), 'utf8'),
  readFile(new URL('./SkillsWorkbench.vue', import.meta.url), 'utf8'),
  readFile(new URL('./agentkit/AISkillCatalogList.vue', import.meta.url), 'utf8'),
])

test('exposes Skills as a first-class workbench surface', () => {
  assert.match(workbench, /<AISkillCatalogList/)
  assert.match(skillCatalog, /title: 'Installed'/)
  assert.match(skillCatalog, /placeholder="Search skills"/)
  assert.match(workbench, /role="dialog"/)
  assert.match(workbench, /role="switch"/)
  assert.match(workbench, /aria-modal="true"/)
  assert.match(workbench, /closeSkillDetail/)
  assert.match(workbench, /toggleSelectedSkill/)
  assert.match(workbench, /<Plug class="h-7 w-7"/)
  assert.match(app, /id: 'builtin:skills'/)
  assert.match(app, /icon: Plug/)
  assert.match(app, /activeWorkbenchTab\?\.kind === 'skills'/)
  assert.match(app, /<SkillsWorkbench/)
})

test('keeps the composer free of explicit skill selection controls', () => {
  assert.doesNotMatch(app, /AssistantSkillsPicker/)
  assert.doesNotMatch(app, /Choose skills/)
  assert.doesNotMatch(app, /selectedAssistantSkillIDs/)
  assert.doesNotMatch(workbench, /Create skill/)
  assert.doesNotMatch(workbench, /Import/)
  assert.doesNotMatch(workbench, /Export/)
  assert.doesNotMatch(workbench, /Delete/)
  assert.doesNotMatch(workbench, /formOpen/)
})

test('uses the generic detail and activation routes for focused skills', () => {
  assert.match(api, /projectURL\(ctx, name, 'skill-detail'\)\}\?id=\$\{encodeURIComponent\(id\)\}/)
  assert.match(api, /projectURL\(ctx, name, 'skills-activation'\)/)
  assert.match(workbench, /api\.getAssistantSkillDetail\(requestContext, projectName, skill\.id\)/)
  assert.match(workbench, /api\.setAssistantSkillActivation\(props\.ctx, props\.projectName, skill\.id, skill\.enabled === false\)/)
})

test('preserves loading, empty, error, and refresh states', () => {
  assert.match(skillCatalog, /Loading skills…/)
  assert.match(workbench, /No skills are installed for this project yet\./)
  assert.match(workbench, /role="alert"/)
  assert.match(workbench, /<InlineNotification[\s\S]*action-label="Retry"[\s\S]*action-busy-label="Retrying skills…"[\s\S]*:action-busy="catalogLoading \|\| actionBusy \|\| props\.loading"/)
  assert.match(workbench, /computed\(\(\) => catalogFailure\.value\s+\? 'No skills were listed in the last successful catalog\.'/)
  assert.match(workbench, /<AISkillCatalogList\s+v-if="props\.hasSnapshot"/)
  assert.match(workbench, /'Skill count is unavailable until the catalog loads\.'/)
  assert.match(workbench, /aria-label="Refresh skills"/)
  const refreshStart = workbench.indexOf('async function refreshCatalog')
  const refreshEnd = workbench.indexOf('async function toggleSelectedSkill', refreshStart)
  const refreshBody = workbench.slice(refreshStart, refreshEnd)
  assert.match(refreshBody, /emit\('catalogUpdated', response\)/)
  assert.match(refreshBody, /return false/)
  assert.match(refreshBody, /assistantSkillsRequestIsCurrent\(requestScope, catalogRequestSerial, props\.projectName, props\.ctx\)/)
  assert.ok((refreshBody.match(/assistantSkillsRequestIsCurrent\(requestScope, catalogRequestSerial, props\.projectName, props\.ctx\)/g) || []).length >= 3)
  assert.match(refreshBody, /catalogError\.value = null/)
  assert.match(refreshBody, /catalogError\.value = friendlyError\(error, 'Could not refresh the skill catalog\.'\)/)
  assert.doesNotMatch(refreshBody, /clearManagementError\(\)/)
})

test('keeps warning-bearing catalogs labeled as listed results and preserves unverified selection', () => {
  assert.match(workbench, /const catalogWarnings = ref\(\[\.\.\.props\.warnings\]\)/)
  assert.match(workbench, /const catalogIncomplete = computed\(\(\) => props\.hasSnapshot && catalogWarnings\.value\.length > 0\)/)
  assert.match(workbench, /:incomplete="catalogIncomplete"/)
  assert.match(workbench, /title="Catalog needs attention"/)
  assert.match(workbench, /Returned skills remain usable, but the list may be incomplete\./)
  assert.match(workbench, /else if \(!catalogWarnings\.value\.length\) clearSelectedSkill\(\)/)
  assert.match(workbench, /selectedSkillUnverified/)
  assert.match(workbench, /Skill not listed in the latest catalog response\./)
  assert.match(workbench, /Retry to verify this skill before changing activation\./) // The stale selection is visible, but never actionable.
  assert.match(workbench, /:aria-disabled="catalogLoading \|\| actionBusy \|\| props\.loading \|\| !projectName \? 'true' : undefined"/)
  const skillSwitchStart = workbench.indexOf('role="switch"')
  const skillSwitchEnd = workbench.indexOf('</button>', skillSwitchStart)
  const skillSwitch = workbench.slice(skillSwitchStart, skillSwitchEnd)
  assert.match(skillSwitch, /:aria-disabled="actionBusy \|\| selectedSkillUnverified \? 'true' : undefined"/)
  assert.doesNotMatch(skillSwitch, /:disabled=/)
  assert.match(skillCatalog, /incomplete\?: boolean/)
  assert.match(skillCatalog, /\$\{filteredSkills\.value\.length\} \$\{props\.query\.trim\(\) \? 'shown' : 'listed'\}/)
  assert.match(skillCatalog, /No listed skills match/)
  assert.match(skillCatalog, /No skills were listed in this response\. Review the warnings or retry\./)
})

test('clears the parent catalog failure only after applying a current catalog response', () => {
  const responseStart = app.indexOf('function applyAssistantSkillsCatalogResponse')
  const responseEnd = app.indexOf('\nasync function loadAssistantSkills', responseStart)
  const responseBody = app.slice(responseStart, responseEnd)
  assert.match(responseBody, /applyAssistantSkillsCatalog\(response\.skills\)/)
  assert.match(responseBody, /assistantSkillsHasSnapshot\.value = true/)
  assert.match(responseBody, /assistantSkillsError\.value = null/)
  assert.match(app, /assistantSkillsHasSnapshot\.value = false/)
  assert.match(app, /applyAssistantSkillsCatalogResponse\(catalog\)/)
  assert.match(app, /:has-snapshot="assistantSkillsHasSnapshot"/)
  const loadStart = app.indexOf('async function loadAssistantSkills')
  const loadTry = app.indexOf('  try {', loadStart)
  assert.doesNotMatch(app.slice(loadStart, loadTry), /assistantSkillsError\.value = null/)
})

test('keeps activation feedback scoped to the changing skill and preserves detail content', () => {
  assert.match(workbench, /const activationSkillID = ref\(''\)/)
  const activationStart = workbench.indexOf('async function toggleSelectedSkill()')
  const activationEnd = workbench.indexOf('\nfunction friendlyError', activationStart)
  const activationBody = workbench.slice(activationStart, activationEnd)
  assert.match(activationBody, /const skill = selectedSkill\.value/)
  assert.match(activationBody, /activationSkillID\.value = skill\.id/)
  assert.match(activationBody, /api\.setAssistantSkillActivation\(props\.ctx, props\.projectName, skill\.id, skill\.enabled === false\)/)
  assert.match(workbench, /Saving…/)
  assert.match(activationBody, /refreshCatalog\(skill\.id, \{ preserveDetail: true \}\)/)
  assert.match(workbench, /options\.preserveDetail && selectedSkillID\.value === selectSkillID/)
  assert.match(workbench, /:aria-busy="activationSkillID === selectedSkill\.id/)
})

test('rejects stale catalog responses when serial, project, or context changes', () => {
  const oldContext = { token: 'old' }
  const currentContext = { token: 'current' }
  const scope = { serial: 7, projectName: 'old-project', ctx: oldContext }

  assert.equal(assistantSkillsRequestIsCurrent(scope, 7, 'old-project', oldContext), true)
  assert.equal(assistantSkillsRequestIsCurrent(scope, 8, 'old-project', oldContext), false)
  assert.equal(assistantSkillsRequestIsCurrent(scope, 7, 'current-project', oldContext), false)
  assert.equal(assistantSkillsRequestIsCurrent(scope, 7, 'old-project', currentContext), false)
})

test('binds search input to the filtered catalog and exposes a clear action', () => {
  assert.match(workbench, /:query="query"[\s\S]*@update:query="query = \$event"/)
  const searchLabelStart = skillCatalog.indexOf('<label class="k-ai-search k-ai-search--clearable">')
  const searchLabelEnd = skillCatalog.indexOf('</label>', searchLabelStart)
  assert.ok(searchLabelStart >= 0 && searchLabelEnd > searchLabelStart)
  assert.doesNotMatch(skillCatalog.slice(searchLabelStart, searchLabelEnd), /flex-1/)
  assert.match(skillCatalog, /class="k-input k-ai-search__input"/)
  assert.match(skillCatalog, /role="status" aria-live="polite">\{\{ loading && skills\.length \? 'Refreshing skills…' : '' \}\}<\/span>/)
  assert.match(skillCatalog, /class="k-ai-skill-catalog__region" :aria-busy="loading \? 'true' : undefined"/)
  assert.match(skillCatalog, /filterAssistantSkills\(props\.skills, props\.query\)/)
  assert.match(skillCatalog, /v-for="skill in filteredSkills"/)
  assert.match(skillCatalog, /aria-label="Clear skill search"/)
  assert.match(skillCatalog, /@click="clearSearch"/)
})

test('keeps skill buttons native and contains detail focus inside the provider overlay', () => {
  assert.match(skillCatalog, /<ul v-else[\s\S]*<li v-for="skill in filteredSkills"[\s\S]*<button[\s\S]*:aria-haspopup="detailPresentation === 'dialog' \? 'dialog' : undefined"/)
  assert.match(workbench, /detail-presentation="dialog"/)
  assert.doesNotMatch(skillCatalog, /<button[\s\S]{0,300}role="listitem"/)
  assert.match(workbench, /<Teleport to="#app-studio-overlay-root">/)
  assert.match(workbench, /\[z-index:var\(--app-studio-z-modal-backdrop\)\]/)
  assert.match(workbench, /ref="detailDialogRef"[\s\S]*role="dialog"[\s\S]*aria-modal="true"[\s\S]*@keydown="handleDetailKeydown"/)
  assert.match(workbench, /detailCloseRef\.value\?\.focus\(\{ preventScroll: true \}\)/)
  assert.match(workbench, /ref="detailCloseRef"[\s\S]*app-studio-touch-target/)
  assert.match(workbench, /event\.key !== 'Tab'[\s\S]*document\.activeElement === first[\s\S]*document\.activeElement === last/)
  assert.match(workbench, /detailOpener = document\.activeElement instanceof HTMLElement/)
  assert.match(workbench, /if \(opener\?\.isConnected\) opener\.focus\(\{ preventScroll: true \}\)/)
  assert.match(workbench, /document\.querySelector<HTMLElement>\('\[data-app-studio-workspace\]'\)/)
  assert.match(workbench, /inertWorkspace\.inert = true/)
  assert.match(workbench, /if \(inertWorkspace && !workspaceWasInert\) inertWorkspace\.inert = false/)
  assert.match(workbench, /function retrySkillDetail\(\)[\s\S]*loadDetail\(skill\)/)
  assert.match(workbench, /v-if="detailError" ref="detailErrorNotice"[\s\S]*<InlineNotification[\s\S]*action-busy-label="Retrying detail…"[\s\S]*:action-busy="detailLoading"/)
  assert.match(workbench, /detailError\.value = null[\s\S]*focusWasLost\(\).*detailCloseRef\.value\?\.focus/s)
  const detailStart = workbench.indexOf('async function loadDetail')
  const detailEnd = workbench.indexOf('\nfunction retrySkillDetail', detailStart)
  const detailBody = workbench.slice(detailStart, detailEnd)
  const detailCatch = detailBody.slice(detailBody.indexOf('} catch (error)'))
  assert.match(detailCatch, /props\.projectName !== projectName/)
  assert.match(detailCatch, /props\.ctx !== requestContext/)
  assert.match(detailCatch, /selectedSkillID\.value !== skillID/)
  assert.match(workbench, /props\.projectName !== projectName[\s\S]*props\.ctx !== requestContext[\s\S]*selectedSkillID\.value !== skillID/)
  assert.match(workbench, /props\.projectName !== projectName[\s\S]*props\.ctx !== requestContext[\s\S]*selectedSkillID\.value !== selectedID/)
})

test('keeps the skill switch compact inside a full touch target', () => {
  assert.match(workbench, /role="switch"[\s\S]*class="app-studio-touch-target flex h-11 shrink-0 items-center justify-center gap-2.5 rounded-md bg-transparent px-1 text-\[12px\] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent\/40/)
  assert.match(workbench, /class="k-toggle" aria-hidden="true" :aria-checked="selectedSkill\.enabled !== false"/)
  assert.match(workbench, /class="k-toggle__knob"/)
  assert.match(workbench, /aria-label="Enable skill"/)
  assert.match(workbench, /'Skill disabled' : 'Skill enabled'/)
  assert.match(workbench, /@container \(max-width: 540px\)[\s\S]*grid-template-columns: 56px minmax\(0, 1fr\)[\s\S]*grid-column: 1 \/ -1/)
  assert.doesNotMatch(workbench, /skill-detail-title[^\n]*truncate/)
})

// Exercise the actual retry handler, including the order that re-enables the
// modal controls. Browser checks separately cover Vue's rendered focus trap.
async function retryHarness() {
  const { default: ts } = await import('typescript')
  const { createContext, runInContext } = await import('node:vm')
  const start = workbench.indexOf('async function retryDetailCatalog()')
  const end = workbench.indexOf('\nasync function toggleSelectedSkill()', start)
  const handler = ts.transpileModule(workbench.slice(start, end), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
  let resolve, requests = 0
  const pending = new Promise(done => { resolve = done })
  const body = {}, retry = { isConnected: true }, outside = { isConnected: true }
  const context = createContext({
    props: { projectName: 'review', ctx: {}, loading: false },
    actionBusy: { value: false }, catalogLoading: { value: false },
    catalogRequestSerial: 0, selectedSkillID: { value: 'skill' },
    catalogFailure: { value: null },
    detailCatalogWarningNotice: { value: { contains: node => node === retry } },
    detailCatalogErrorNotice: { value: { contains: node => node === retry, querySelector: () => errorRetry } },
    detailCloseRef: { value: { focus: () => {
      assert.equal(context.actionBusy.value, false, 'Close must be enabled before restoring focus')
      context.document.activeElement = close
    } } },
    document: { activeElement: retry, body },
    nextTick: async () => { if (context.document.activeElement === retry) context.document.activeElement = body },
    focusWasLost: () => context.document.activeElement === body,
    assistantSkillsRequestIsCurrent,
    refreshCatalog: () => { requests++; context.catalogRequestSerial++; return pending },
  })
  const close = {}, errorRetry = { focus: () => { context.document.activeElement = errorRetry } }
  runInContext(handler, context)
  return { context, resolve, retry, outside, close, errorRetry, requests: () => requests }
}

test('detail catalog retry serializes activation and restores focus after enabling Close', async () => {
  const h = await retryHarness()
  const first = h.context.retryDetailCatalog()
  await h.context.retryDetailCatalog()
  assert.equal(h.requests(), 1)
  assert.equal(h.context.actionBusy.value, true)
  assert.equal(h.context.document.activeElement, h.retry)
  h.resolve(true)
  await first
  assert.equal(h.context.document.activeElement, h.close)
})

test('a failed warning retry moves lost focus to the single error retry', async () => {
  const h = await retryHarness()
  const request = h.context.retryDetailCatalog()
  h.context.catalogFailure.value = 'Source unavailable'
  h.resolve(false)
  await request
  assert.equal(h.context.document.activeElement, h.errorRetry)
  assert.equal(h.context.actionBusy.value, false)
})

test('retry does not steal focus when a warning remains or the user moves focus', async () => {
  for (const target of ['retry', 'outside']) {
    const h = await retryHarness()
    h.context.nextTick = async () => {}
    const request = h.context.retryDetailCatalog()
    h.context.document.activeElement = h[target]
    h.resolve(true)
    await request
    assert.equal(h.context.document.activeElement, h[target])
  }
})

test('obsolete modal retry cannot clear newer busy state or move focus', async () => {
  for (const change of ['project', 'context', 'serial', 'selection']) {
    const h = await retryHarness()
    const request = h.context.retryDetailCatalog()
    if (change === 'project') h.context.props.projectName = 'another-project'
    if (change === 'context') h.context.props.ctx = {}
    if (change === 'serial') h.context.catalogRequestSerial++
    if (change === 'selection') h.context.selectedSkillID.value = 'another-skill'
    h.resolve(true)
    await request
    assert.notEqual(h.context.document.activeElement, h.close)
    if (change !== 'selection') assert.equal(h.context.actionBusy.value, true)
  }
})
