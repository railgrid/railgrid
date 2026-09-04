/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import * as ts from 'typescript'

const components = path.dirname(new URL(import.meta.url).pathname)
const appLayout = fs.readFileSync(path.join(components, 'AppLayout.vue'), 'utf8')
const helpSupportModal = fs.readFileSync(path.join(components, 'HelpSupportModal.vue'), 'utf8')
const providerNavOverflow = fs.readFileSync(path.join(components, 'ProviderNavOverflow.vue'), 'utf8')
const accountMenu = fs.readFileSync(path.join(components, 'AccountAccessMenu.vue'), 'utf8')
const sidebarExpansion = fs.readFileSync(path.join(components, '..', 'composables', 'useSidebarExpansion.ts'), 'utf8')
const navigationDock = fs.readFileSync(path.join(components, '..', 'composables', 'useNavigationDock.ts'), 'utf8')
const shellNavigationSource = fs.readFileSync(path.join(components, '..', 'lib', 'shellNavigation.ts'), 'utf8')
const mainCss = fs.readFileSync(path.join(components, '..', 'assets', 'main.css'), 'utf8')
const shellNavigation = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(ts.transpileModule(shellNavigationSource, {
  compilerOptions: {
    module: ts.ModuleKind.ESNext,
    target: ts.ScriptTarget.ES2020,
  },
}).outputText)}`)
const finiteDockPositionSource = navigationDock.match(/export function isFiniteDockPosition[\s\S]*?\n}\n/)?.[0]
assert.ok(finiteDockPositionSource, 'dock position validation helper should remain directly testable')
const finiteDockPositionModule = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(ts.transpileModule(finiteDockPositionSource, {
  compilerOptions: {
    module: ts.ModuleKind.ESNext,
    target: ts.ScriptTarget.ES2020,
  },
}).outputText)}`)
const clampDockPositionSource = navigationDock.match(/export function clampDockPosition[\s\S]*?\n}\n/)?.[0]
assert.ok(clampDockPositionSource, 'dock viewport clamping helper should remain directly testable')
const clampDockPositionModule = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(ts.transpileModule(clampDockPositionSource, {
  compilerOptions: {
    module: ts.ModuleKind.ESNext,
    target: ts.ScriptTarget.ES2020,
  },
}).outputText)}`)
const { flattenProviderItems, hasActiveNavRoute, isActiveRoute, isProviderItemActive, providerFamilyItems } = shellNavigation
const { isFiniteDockPosition } = finiteDockPositionModule
const { clampDockPosition } = clampDockPositionModule

test('ordinary pages use the shared fluid desktop column without becoming full bleed', () => {
  assert.match(appLayout, /'relative z-10 min-h-0 min-w-0 flex-1'/)
  assert.match(appLayout, /layoutProps\.fullBleed \? 'h-full min-h-0' : 'w-full'/)
  assert.doesNotMatch(appLayout, /max-w-(?:5xl|7xl)/)
})

test('flat shell modes flatten provider children with qualified labels', () => {
  assert.match(shellNavigationSource, /export function flattenProviderItems\(items: ProviderNavEntry\[\]\)/)
  assert.match(shellNavigationSource, /label: `\$\{item\.label\} \/ \$\{child\.label\}`/)
  assert.match(shellNavigationSource, /exact: Boolean\(item\.children\?\.length\)/)
  assert.ok((appLayout.match(/isActive\(item\.to, item\.exact\)/g) ?? []).length >= 4)
  assert.match(appLayout, /items: flattenProviderItems\(g\.items\)/)
  assert.match(appLayout, /items: flattenProviderItems\(cat\.uncategorized\)/)
  assert.match(appLayout, /<!-- HORIZONTAL BAR \(top or bottom\) -->/)
  assert.match(appLayout, /<!-- FLOATING MODE \(also shown during drag\) -->/)
})

test('pure navigation helpers keep parent and child routes to one current item', () => {
  const entries = [
    {
      label: 'Edges',
      to: '/providers/edges',
      children: [{ label: 'Workloads', to: '/providers/edges/workloads' }],
    },
  ]
  const flatEntries = flattenProviderItems(entries)

  const childRouteActive = flatEntries.filter((item) => isActiveRoute('/providers/edges/workloads', item.to, item.exact))
  assert.equal(childRouteActive.length, 1)
  assert.equal(childRouteActive[0].to, '/providers/edges/workloads')

  const parentRouteActive = flatEntries.filter((item) => isActiveRoute('/providers/edges', item.to, item.exact))
  assert.equal(parentRouteActive.length, 1)
  assert.equal(parentRouteActive[0].to, '/providers/edges')
  assert.equal(isProviderItemActive('/providers/edges/workloads', entries[0]), false)
  assert.equal(hasActiveNavRoute('/providers/edges/workloads', entries), true)
  assert.deepEqual(providerFamilyItems('/providers/edges/workloads', flatEntries).map((item) => item.to), [
    '/providers/edges',
    '/providers/edges/workloads',
  ])
})

test('flat docks keep the active family inline and put inactive providers in a categorized menu', () => {
  assert.ok((appLayout.match(/<ProviderNavOverflow :sections="horizontalProviderSections" \/>/g) ?? []).length >= 2)
  assert.match(appLayout, /const horizontalProviderSections = computed\(\(\) =>/)
  assert.match(providerNavOverflow, /role="menu"/)
  assert.match(providerNavOverflow, /Browse other providers/)
  assert.match(providerNavOverflow, /section\.items\.filter\(\(item\) => item\.familyKey !== activeFamilyKey\.value\)/)
  assert.match(providerNavOverflow, /role="menuitem"/)
})

test('provider overflow preserves touch targets, route cleanup, and native-equivalent menu activation', () => {
  assert.match(providerNavOverflow, /class="shell-provider-family-link shell-nav-link/)
  assert.match(providerNavOverflow, /\.shell-provider-family-link,[\s\S]*min-height: 44px;[\s\S]*min-width: 44px;/)
  assert.match(providerNavOverflow, /watch\(\(\) => route\.fullPath, \(\) => close\(\)\)/)
  assert.doesNotMatch(providerNavOverflow, /window\.addEventListener|document\.addEventListener/)

  const triggerStart = providerNavOverflow.indexOf('function onTriggerClick')
  const triggerEnd = providerNavOverflow.indexOf('\n}\n\nfunction onMenuKeydown', triggerStart)
  assert.ok(triggerStart >= 0 && triggerEnd > triggerStart)
  assert.match(providerNavOverflow, /@click="onTriggerClick"/)
  assert.match(providerNavOverflow.slice(triggerStart, triggerEnd), /if \(open\.value\) close\(\)[\s\S]*else openMenu\(0\)/)
  assert.match(providerNavOverflow, /if \(!open\.value\) toggle\(\)/)

  const menuItemKeydownStart = providerNavOverflow.indexOf('function onMenuItemKeydown')
  const menuItemKeydownEnd = providerNavOverflow.indexOf('\n}\n\nfunction itemLabel', menuItemKeydownStart)
  assert.ok(menuItemKeydownStart >= 0 && menuItemKeydownEnd > menuItemKeydownStart)
  const menuItemKeydown = providerNavOverflow.slice(menuItemKeydownStart, menuItemKeydownEnd)
  assert.match(menuItemKeydown, /event\.key !== ' ' && event\.code !== 'Space'/)
  assert.match(menuItemKeydown, /event\.preventDefault\(\)/)
  assert.match(menuItemKeydown, /target\.click\(\)/)
  assert.doesNotMatch(menuItemKeydown, /event\.key === 'Enter'/)
  assert.match(providerNavOverflow, /role="menuitem"[\s\S]*@keydown="onMenuItemKeydown"/)
})

test('vertical navigation exposes landmarks and complete route/group state', () => {
  assert.match(appLayout, /<nav aria-label="Primary navigation"/)
  assert.match(appLayout, /:aria-current="isActive\(item\.to, item\.exact\) \? 'page' : undefined"/)
  assert.match(appLayout, /:aria-current="isActive\(child\.to\) \? 'page' : undefined"/)
  assert.match(appLayout, /:aria-expanded="isNavGroupOpen\('cat:' \+ group\.name, group\.items\)"/)
  assert.match(appLayout, /:aria-controls="navGroupPanelId\('cat:' \+ group\.name\)"/)
  assert.match(appLayout, /:aria-expanded="isNavGroupOpen\('item:' \+ item\.to, item\.children\)"/)
  assert.match(appLayout, /:aria-controls="navGroupPanelId\('item:' \+ item\.to\)"/)
})

test('vertical Providers catalog item uses the same readable primary treatment as Dashboard', () => {
  const providersStart = appLayout.indexOf('<!-- Providers catalog is the primary destination immediately below')
  const categoriesStart = appLayout.indexOf('<!-- Provider categories render as non-clickable section dividers:', providersStart)
  assert.ok(providersStart >= 0 && categoriesStart > providersStart)
  const providersLink = appLayout.slice(providersStart, categoriesStart)

  assert.match(providersLink, /:to="providersHeaderItem\.to"/)
  assert.match(providersLink, /class="shell-nav-link flex items-center gap-2\.5 rounded-md px-3 py-2 text-\[11px\] font-medium transition-colors duration-200"/)
  assert.doesNotMatch(providersLink, /\buppercase\b/)
  assert.doesNotMatch(providersLink, /\btracking-wider\b/)
  assert.match(providersLink, /:aria-current="isActive\(providersHeaderItem\.to, true\) \? 'page' : undefined"/)
  assert.match(providersLink, /:title="sidebarExpanded \? undefined : providersHeaderItem\.label"/)
  assert.match(providersLink, /:aria-label="sidebarExpanded \? undefined : providersHeaderItem\.label"/)
})

test('dock movement supports pointer input and keyboard placement', () => {
  assert.match(appLayout, /@pointerdown="onDragStart"/)
  assert.match(navigationDock, /window\.addEventListener\('pointermove', onDragMove\)/)
  assert.match(navigationDock, /window\.addEventListener\('pointerup', onDragEnd\)/)
  assert.match(navigationDock, /window\.addEventListener\('pointercancel', onDragEnd\)/)
  assert.match(navigationDock, /window\.removeEventListener\('pointermove', onDragMove\)/)
  assert.match(navigationDock, /window\.removeEventListener\('pointerup', onDragEnd\)/)
  assert.match(navigationDock, /window\.removeEventListener\('pointercancel', onDragEnd\)/)
  assert.match(navigationDock, /setLayoutInsets\(\{ left: '0px', right: '0px', bottom: '0px' \}\)/)
  assert.match(navigationDock, /function onDragHandleKeydown\(event: KeyboardEvent\)/)
  for (const key of ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'PageUp', 'PageDown']) {
    assert.match(navigationDock, new RegExp(key))
  }
  assert.match(navigationDock, /export type DockMode = 'float' \| 'left' \| 'right' \| 'top' \| 'bottom'/)
})

test('persisted floating coordinates reject non-finite and non-number positions', () => {
  assert.match(navigationDock, /if \(!isFiniteDockPosition\(state\) \|\| state\.x < 0 \|\| state\.y < 0\)/)
  assert.match(navigationDock, /typeof value\.x === 'number'/)
  assert.match(navigationDock, /Number\.isFinite\(value\.x\)/)
  assert.match(navigationDock, /typeof value\.y === 'number'/)
  assert.match(navigationDock, /Number\.isFinite\(value\.y\)/)

  for (const position of [
    { x: Number.NaN, y: 12 },
    { x: Number.POSITIVE_INFINITY, y: 12 },
    { x: 12, y: Number.NEGATIVE_INFINITY },
    { x: '12', y: 12 },
    { x: 12, y: '12' },
    { x: null, y: 12 },
    { x: 12, y: undefined },
  ]) {
    assert.equal(isFiniteDockPosition(position), false, `expected invalid position: ${JSON.stringify(position)}`)
  }
  assert.equal(isFiniteDockPosition({ x: 0, y: 0 }), true)
  assert.equal(isFiniteDockPosition({ x: 240, y: 80 }), true)
})

test('custom floating docks remain reachable after viewport and content-size changes', () => {
  assert.deepEqual(
    clampDockPosition(
      { x: 1200, y: 700 },
      { width: 375, height: 667 },
      { width: 340, height: 64 },
    ),
    { x: 35, y: 603 },
  )
  assert.deepEqual(
    clampDockPosition(
      { x: 80, y: 40 },
      { width: 320, height: 480 },
      { width: 480, height: 520 },
    ),
    { x: 0, y: 0 },
  )

  assert.match(navigationDock, /window\.addEventListener\('resize', scheduleFloatingDockClamp\)/)
  assert.match(navigationDock, /window\.addEventListener\('orientationchange', scheduleFloatingDockClamp\)/)
  assert.match(navigationDock, /new ResizeObserver\(scheduleFloatingDockClamp\)/)
  assert.match(navigationDock, /window\.removeEventListener\('resize', scheduleFloatingDockClamp\)/)
  assert.match(navigationDock, /window\.removeEventListener\('orientationchange', scheduleFloatingDockClamp\)/)
  assert.match(navigationDock, /floatResizeObserver\?\.disconnect\(\)/)
})

test('extracted shell ownership stays one-way and fences pointer lifecycles', () => {
  assert.doesNotMatch(shellNavigationSource, /^\s*import\s/m)
  assert.doesNotMatch(appLayout, /window\.addEventListener\('pointer(?:move|up|cancel)'/)
  assert.match(navigationDock, /if \(!isDragging\.value \|\| \(activePointerId !== null && event\.pointerId !== activePointerId\)\) return/)
  assert.match(navigationDock, /if \(event\.button !== 0 \|\| activePointerId !== null\) return/)
})

test('shell recovery and context status are present without claiming unconditional liveness', () => {
  assert.equal((appLayout.match(/aria-label="Open help and community"/g) ?? []).length, 3)
  assert.equal((appLayout.match(/aria-controls="help-support-dialog"/g) ?? []).length, 3)
  assert.equal((appLayout.match(/@click="showHelpModal = true"/g) ?? []).length, 3)
  assert.match(appLayout, /<HelpSupportModal v-if="showHelpModal" @close="showHelpModal = false" \/>/)
  assert.ok((appLayout.match(/@click="retryProviderBindings"/g) ?? []).length >= 3)
  assert.match(appLayout, /providerBindingsStale = computed\(\(\) => providersStore\.bindingsStale\)/)
  assert.match(appLayout, /const contextStatus = computed<ContextStatus>/)
  assert.match(appLayout, /workspaceLoadState === 'idle' \|\| tenantStore\.workspaceLoadState === 'loading'/)
  assert.match(appLayout, /label: 'Loading workspace', live: false, visible: false/)
  assert.match(appLayout, /if \(tenantStore\.activeWorkspace\?\.clusterName\)/)
  assert.match(appLayout, /label: 'Workspace live', live: true, visible: false/)
  assert.match(appLayout, /label: 'Provisioning', live: false, visible: true/)
  assert.doesNotMatch(appLayout, /label: 'Pending'/)

  const brandRowStart = appLayout.indexOf('<div class="shell-vertical-brand-row')
  const opsRowStart = appLayout.indexOf('<div v-if="contextStatus.visible" class="shell-vertical-ops-row', brandRowStart)
  assert.ok(brandRowStart >= 0 && opsRowStart > brandRowStart)
  const brandRow = appLayout.slice(brandRowStart, opsRowStart)
  assert.ok(brandRow.indexOf('shell-drag-handle') < brandRow.indexOf('<Hexagon'))
  assert.ok(brandRow.indexOf('<Hexagon') < brandRow.indexOf('shell-brand-name'))
  assert.ok(brandRow.indexOf('shell-brand-name') < brandRow.indexOf('<PanelLeftClose'))

  const opsRowEnd = appLayout.indexOf('</template>', opsRowStart)
  const opsRow = appLayout.slice(opsRowStart, opsRowEnd)
  assert.match(opsRow, /shell-context-status shell-context-status--vertical/)
  assert.doesNotMatch(opsRow, /shell-drag-handle/)

  const collapsedHeaderStart = appLayout.indexOf('<div v-else class="shell-vertical-collapsed-header')
  const collapsedHeaderEnd = appLayout.indexOf('\n\n      <div class="mx-2 my-2 h-px', collapsedHeaderStart)
  assert.ok(collapsedHeaderStart >= 0 && collapsedHeaderEnd > collapsedHeaderStart)
  const collapsedHeader = appLayout.slice(collapsedHeaderStart, collapsedHeaderEnd)
  assert.ok(collapsedHeader.indexOf('shell-drag-handle') < collapsedHeader.indexOf('<Hexagon'))
  assert.ok(collapsedHeader.indexOf('<Hexagon') < collapsedHeader.indexOf('<PanelLeftOpen'))
  assert.match(collapsedHeader, /v-if="contextStatus\.visible"[\s\S]*shell-context-status flex h-4 w-4/)
  assert.ok(collapsedHeader.indexOf('<PanelLeftOpen') < collapsedHeader.indexOf('shell-context-status'))

  assert.match(appLayout, /<div\s+v-if="contextStatus\.visible"\s+class="shell-context-status flex items-center/)
  assert.match(appLayout, /<div\s+v-if="contextStatus\.visible"\s+class="shell-context-status flex shrink-0 items-center/)
  assert.equal((appLayout.match(/v-if="contextStatus\.visible"/g) ?? []).length, 4)
  assert.doesNotMatch(appLayout, /class="mb-1 flex items-center gap-2 px-2" :class="sidebarExpanded \? '' : 'flex-col gap-1\.5 px-0'"/)
  assert.doesNotMatch(appLayout, /shell-context-status shell-context-status--vertical[^"]*(?:rounded|border|bg-)/)
  assert.ok((appLayout.match(/text-\[10px\] font-semibold uppercase tracking-widest/g) ?? []).length >= 3)
  assert.equal((appLayout.match(/class="shell-binding-status[^\"]*text-\[10px\]/g) ?? []).length, 3)
  assert.doesNotMatch(appLayout, /class="shell-binding-status[^\"]*text-\[(?:7|8|9)px\]/)
  assert.doesNotMatch(appLayout, /shell-context-status[\s\S]{0,120}text-\[(?:7|8|9)px\] font-semibold uppercase tracking-widest/)
  assert.match(appLayout, /@media \(pointer: coarse\)/)
  assert.match(appLayout, /min-height: 44px/)
})

test('help modal prioritizes docs and keeps community support accessible', () => {
  assert.match(helpSupportModal, /const docsURL = 'https:\/\/faros\.sh\/docs\/'/)
  assert.match(helpSupportModal, /const discordURL = 'https:\/\/discord\.gg\/VjUA7zyhC'/)
  assert.match(helpSupportModal, /const issuesURL = 'https:\/\/github\.com\/faroshq\/faros\/issues'/)
  assert.match(helpSupportModal, /role="dialog"/)
  assert.match(helpSupportModal, /id="help-support-dialog"/)
  assert.match(helpSupportModal, /aria-modal="true"/)
  assert.match(helpSupportModal, /aria-labelledby="help-support-title"/)
  assert.match(helpSupportModal, /aria-describedby="help-support-description"/)
  assert.match(helpSupportModal, /@click\.self="\$emit\('close'\)"/)
  assert.match(helpSupportModal, /useEscapeKey\(\(\) => emit\('close'\)\)/)
  assert.match(helpSupportModal, /nextTick\(\(\) => closeButton\.value\?\.focus\(\)\)/)
  assert.match(helpSupportModal, /nextTick\(\(\) => target\?\.isConnected && target\.focus\(\)\)/)
  assert.match(helpSupportModal, /event\.shiftKey && document\.activeElement === first/)
  assert.match(helpSupportModal, /!event\.shiftKey && document\.activeElement === last/)

  const docs = helpSupportModal.indexOf('Documentation')
  const secondary = helpSupportModal.indexOf('More ways to get help')
  const discord = helpSupportModal.indexOf('Discord community')
  const issues = helpSupportModal.indexOf('GitHub issues')
  assert.ok(docs >= 0 && docs < secondary && secondary < discord && discord < issues)
  assert.equal((helpSupportModal.match(/target="_blank"/g) ?? []).length, 3)
  assert.equal((helpSupportModal.match(/rel="noreferrer noopener"/g) ?? []).length, 3)
  assert.doesNotMatch(helpSupportModal, /systems operational|Contact support|Troubleshooting/)
})

test('dock movement is learnable and account actions name the resulting placement', () => {
  assert.ok((appLayout.match(/:aria-describedby="dockHintId"/g) ?? []).length >= 3)
  assert.match(appLayout, /Drag to an edge · Shift\+Arrow to dock · Enter to float/)
  assert.ok((appLayout.match(/:undock-label="dockActionLabel"/g) ?? []).length >= 3)
  assert.match(appLayout, /dockState\.value\.mode === 'float' \? 'Reset floating position' : 'Float navigation'/)
  assert.match(appLayout, /shell-nav-category-cue/)
  assert.match(appLayout, /:aria-label="`Category: \$\{group\.name\}`"/)
  assert.doesNotMatch(appLayout, /transition-all/)
  assert.doesNotMatch(mainCss, /\.island-nav:hover/)
  assert.doesNotMatch(mainCss, /transition:\s*all\b/)
})

test('narrow flat chrome scrolls as one reachable surface', () => {
  assert.match(appLayout, /class="shell-route-track flex/)
  assert.match(appLayout, /\.faros-shell-horizontal \{[\s\S]*overflow-x: auto;/)
  assert.match(appLayout, /\.faros-shell-horizontal \.shell-route-track,[\s\S]*\.shell-floating-chrome \.shell-route-track \{[\s\S]*flex: 0 0 auto;[\s\S]*min-width: max-content;[\s\S]*overflow: visible;/)
  assert.match(appLayout, /\.shell-floating-chrome \{[\s\S]*overflow-x: auto;/)
})

test('shell preferences tolerate SSR and unavailable browser storage', () => {
  assert.match(appLayout, /function browserStorage\(\): Storage \| null/)
  assert.match(appLayout, /if \(typeof window === 'undefined'\) return null/)
  assert.match(appLayout, /browserStorage\(\)\?\.setItem\(NAV_GROUPS_KEY/)
  assert.match(navigationDock, /function browserStorage\(\): Storage \| null/)
  assert.match(navigationDock, /if \(typeof window === 'undefined'\) return null/)
  assert.match(navigationDock, /browserStorage\(\)\?\.setItem\(DOCK_STORAGE_KEY/)
  assert.match(sidebarExpansion, /if \(typeof window === 'undefined'\) return false/)
  assert.match(sidebarExpansion, /window\.localStorage\.setItem\(SIDEBAR_EXPANDED_KEY/)
})

test('organization chooser marks the complete organizations route family active', () => {
  assert.match(accountMenu, /route\.path === '\/organizations' \|\| route\.path\.startsWith\('\/organizations\/'\)/)
  assert.match(accountMenu, /:aria-current="organizationsActive \? 'page' : undefined"/)
})
