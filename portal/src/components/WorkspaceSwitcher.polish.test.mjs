import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import * as ts from 'typescript'

const source = fs.readFileSync(path.join(path.dirname(new URL(import.meta.url).pathname), 'WorkspaceSwitcher.vue'), 'utf8')
const helperStart = source.indexOf('function recoveryDetail')
const helperEnd = source.indexOf('const organizationFirstLoadMessage', helperStart)
assert.ok(helperStart >= 0 && helperEnd > helperStart)
const helperModule = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(ts.transpileModule(
  `${source.slice(helperStart, helperEnd)}\nexport { recoveryDetail, recoveryMessage }`,
  { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } },
).outputText)}`)

test('horizontal switcher keeps workspace primary and organization provenance visible in one compact row', () => {
  const triggerStart = source.indexOf('<button\n      ref="triggerRef"')
  const triggerEnd = source.indexOf('\n    </button>', triggerStart)
  assert.ok(triggerStart >= 0 && triggerEnd > triggerStart)
  const trigger = source.slice(triggerStart, triggerEnd)

  assert.match(trigger, /variant === 'horizontal'/)
  assert.match(trigger, /v-if="variant === 'horizontal'"/)
  assert.match(trigger, /font-mono text-\[11px\] leading-4 text-text-primary/) // workspace is primary
  assert.match(trigger, /max-w-\[44%\] shrink-0 truncate border-l border-border-subtle\/70 pl-1 text-\[10px\] leading-3 text-text-secondary/)
  assert.match(trigger, /\{\{ orgContextLabel \}\}/)
  assert.match(trigger, /:title="orgContextLabel"/)
  assert.doesNotMatch(trigger, /variant === 'horizontal'[\s\S]{0,220}py-2/)
})

test('compact icon-only switcher has a distinct organization cue without replacing readiness state', () => {
  assert.match(source, /class="workspace-switcher-trigger group relative flex/)
  assert.match(source, /v-if="variant === 'compact' && tenant\.orgUUID"/)
  assert.match(source, /border border-accent\/80 bg-surface-raised/)
  assert.match(source, /:aria-label="workspaceTriggerLabel"/)
  assert.match(source, /:title="variant === 'compact' \? workspaceTriggerLabel : undefined"/)
  assert.match(source, /Organization provenance:/)
  assert.match(source, /v-if="workspaceReady"[\s\S]*?bg-success/)
  assert.match(source, /v-else-if="workspaceTriggerWarning"[\s\S]*?bg-warning/)
  const optionStart = source.indexOf('<button\n              v-for="workspace in filteredWorkspaces"')
  const badgeStart = source.indexOf('class="k-badge shrink-0', optionStart)
  assert.ok(optionStart >= 0 && badgeStart > optionStart)
  const badge = source.slice(badgeStart, source.indexOf('>', badgeStart))
  assert.match(badge, /text-\[10px\]/)
  assert.doesNotMatch(badge, /text-\[8px\]/)
})

test('recovery states use concise truthful copy while retaining verification boundaries and Retry actions', () => {
  assert.match(source, /function recoveryDetail\(error: string \| null\): string/)
  assert.ok(source.includes(".replace(/\\s*Try again\\.?\\s*$/i, '')"))
  assert.match(source, /const organizationRefreshMessage = computed\(\(\) =>/)
  assert.match(source, /last-known organization \(unverified\)/)
  assert.match(source, /no verified organization list is available/)
  assert.match(source, /const workspaceRefreshMessage = computed\(\(\) =>/)
  assert.match(source, /last-known workspaces \(unverified\)/)
  assert.match(source, /last verified workspace list was empty/)
  assert.match(source, /<span>\{\{ organizationRefreshMessage \}\}<\/span>/)
  assert.match(source, /<span>\{\{ workspaceRefreshMessage \}\}<\/span>/)
  assert.equal((source.match(/@click="retryOrganizations"/g) ?? []).length, 2)
  assert.equal((source.match(/@click="retryWorkspaces"/g) ?? []).length, 2)
  assert.doesNotMatch(source, /Showing the last-known organization \(unverified\); workspace switching is paused\./)
  assert.doesNotMatch(source, /Showing last-known workspaces \(unverified\); switching is paused until retry succeeds\./)
})

test('recovery copy removes redundant retry instructions without hiding the server reason', () => {
  assert.equal(
    helperModule.recoveryDetail('Unable to load organizations (HTTP 503). Try again.'),
    'Unable to load organizations (HTTP 503)',
  )
  assert.equal(
    helperModule.recoveryMessage('Unable to load organizations', 'Unable to load organizations (HTTP 503). Try again.'),
    'Unable to load organizations (HTTP 503).',
  )
  assert.equal(
    helperModule.recoveryMessage('Workspaces could not be refreshed; switching is paused', 'failed to list workspaces: 503'),
    'Workspaces could not be refreshed; switching is paused — failed to list workspaces: 503.',
  )
})

test('first-load completion moves focus from the dialog to the selected or first available workspace', () => {
  const watchStart = source.indexOf('watch(open, async (isOpen) => {')
  const watchEnd = source.indexOf('\n})', watchStart)
  assert.ok(watchStart >= 0 && watchEnd > watchStart)
  const openWatch = source.slice(watchStart, watchEnd)
  const load = openWatch.indexOf('await ensureContextLoaded()')
  const postLoadTick = openWatch.indexOf('await nextTick()', load)
  const postLoadFocus = openWatch.indexOf('focusInitialPanelControl()', load)
  assert.ok(load >= 0 && postLoadTick > load && postLoadFocus > postLoadTick)
})
