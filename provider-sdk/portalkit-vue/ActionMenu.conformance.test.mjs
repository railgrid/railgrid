import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'
import { computed, effectScope, nextTick, reactive, ref, watch } from '../../portal/node_modules/vue/index.mjs'

const component = readFileSync(new URL('./ActionMenu.vue', import.meta.url), 'utf8')
const layoutSelector = readFileSync(new URL('./LayoutSelector.vue', import.meta.url), 'utf8')
const stylesheet = readFileSync(new URL('../portalkit/railgrid-ui.css', import.meta.url), 'utf8')
const styles = readFileSync(new URL('../portalkit/styles.ts', import.meta.url), 'utf8')
const resourceTable = readFileSync(new URL('./ResourceTable.vue', import.meta.url), 'utf8')
const cssVersion = stylesheet.match(/--railgrid-ui-core-version:\s*(\d+);/)?.[1]
const runtimeVersion = styles.match(/RAILGRID_UI_CORE_VERSION = (\d+)/)?.[1]
assert.ok(cssVersion, 'canonical stylesheet declares a version')
assert.ok(runtimeVersion, 'style handoff declares a version')

function sourceBlock(source, startMarker, endMarker) {
  const start = source.indexOf(startMarker)
  assert.notEqual(start, -1, `source contains ${startMarker}`)
  const end = endMarker ? source.indexOf(endMarker, start) : source.length
  assert.notEqual(end, -1, `source contains ${endMarker}`)
  return source.slice(start, end)
}

function templateBlock(source, marker) {
  const start = source.indexOf(marker)
  assert.notEqual(start, -1, `template contains ${marker}`)
  const end = source.indexOf('</button>', start)
  assert.notEqual(end, -1, `template button closes after ${marker}`)
  return source.slice(start, end + '</button>'.length)
}

function styleNode(id, textContent = '') {
  const attributes = new Map()
  return {
    id,
    textContent,
    setAttribute(name, value) {
      attributes.set(name, value)
    },
    getAttribute(name) {
      return attributes.get(name) ?? null
    },
  }
}

function executableStylesHelper({ canonical = '1', version = '', existingNodes = [] } = {}) {
  const computedValues = new Map([
    ['--railgrid-ui-canonical', canonical],
    ['--railgrid-ui-core-version', version],
    ['--railgrid-ui-version', version],
  ])
  const nodes = new Map(existingNodes.map(node => [node.id, node]))
  const document = {
    documentElement: {
      style: {
        getPropertyValue(name) {
          return computedValues.get(name) ?? ''
        },
      },
    },
    getElementById(id) {
      return nodes.get(id) ?? null
    },
    createElement(tagName) {
      assert.equal(tagName, 'style')
      return styleNode('')
    },
    head: {
      children: [],
      appendChild(node) {
        this.children.push(node)
        nodes.set(node.id, node)
        // A real browser incorporates the newly appended stylesheet into the
        // computed root style before a second provider bundle can run.
        computedValues.set('--railgrid-ui-canonical', '1')
        computedValues.set('--railgrid-ui-core-version', node.getAttribute('data-railgrid-ui-core-version') ?? '')
        return node
      },
    },
  }
  const context = { document, window: { getComputedStyle: () => ({ getPropertyValue: name => computedValues.get(name) ?? '' }) } }
  const executable = styles
    .replace(/^import railgridUIStyles.*$/m, "const railgridUIStyles = 'current-railgrid-ui';")
    .replaceAll('export const ', 'const ')
    .replaceAll('export function ', 'function ')
    .replaceAll(': string', '')
    .replaceAll(': boolean', '')
    .replaceAll(': void', '')
    + '\n;globalThis.__styles = { ensureRailgridUIStyles, RAILGRID_UI_STYLE_ID, RAILGRID_UI_VERSION };'
  runInNewContext(executable, context)
  return { ...context.__styles, document }
}

test('ActionMenu exposes a typed action and accessible menu contract', () => {
  assert.match(component, /export interface ActionMenuItem\s*\{[\s\S]*id: string[\s\S]*label: string[\s\S]*tone\?: ActionMenuTone[\s\S]*disabled\?: boolean[\s\S]*busy\?: boolean/)
  assert.match(component, /const emit = defineEmits<[\s\S]*select: \[id: string\]/)
  assert.match(component, /function select\(id: string\)/)
  assert.match(component, /aria-haspopup="menu"/)
  assert.match(component, /:aria-expanded="open"/)
  assert.match(component, /:aria-controls="menuID"/)
  assert.match(component, /role="menu"/)
  assert.match(component, /role="menuitem"/)
  assert.match(component, /:aria-disabled="item\.disabled \|\| item\.busy \? 'true' : undefined"/)
  assert.match(component, /:aria-busy="item\.busy \? 'true' : undefined"/)
  assert.match(component, /:tabindex="index === activeIndex && isSelectable\(index\) \? 0 : -1"/)
})

test('ActionMenu owns complete keyboard and dismissal behavior', () => {
  for (const key of ['ArrowDown', 'ArrowUp', 'Home', 'End', 'Enter', ' ', 'Spacebar', 'Escape', 'Tab']) {
    assert.match(component, new RegExp(`event\\.key === '${key === ' ' ? ' ' : key}'`), `handles ${key}`)
  }
  assert.match(component, /function moveActive\(direction: 1 \| -1\)/)
  assert.match(component, /function closeMenuAfterTab\(\)/)
  assert.match(component, /document\.addEventListener\('pointerdown', closeFromOutsidePointer, true\)/)
  assert.match(component, /document\.addEventListener\('focusin', closeFromOutsideFocus\)/)
  assert.match(component, /closeMenu\(true\)/)
})

test('ActionMenu renders tones and keeps disabled or busy items out of the roving set', () => {
  assert.match(component, /export type ActionMenuTone = 'neutral' \| 'accent' \| 'warning' \| 'danger'/)
  const itemType = sourceBlock(component, 'export interface ActionMenuItem', '}\n\nconst props')
  for (const field of ['id: string', 'label: string', 'tone\\?: ActionMenuTone', 'disabled\\?: boolean', 'busy\\?: boolean']) {
    assert.match(itemType, new RegExp(field), `typed item field ${field}`)
  }

  const itemTemplate = templateBlock(component, '<button\n            type="button"\n            class="k-menu-item k-action-menu__item"')
  assert.match(itemTemplate, /:class="item\.tone \? `k-menu-item--\$\{item\.tone\}` : undefined"/)
  assert.match(itemTemplate, /:disabled="item\.disabled \|\| item\.busy"/)
  assert.match(itemTemplate, /:aria-disabled="item\.disabled \|\| item\.busy \? 'true' : undefined"/)
  assert.match(itemTemplate, /:aria-busy="item\.busy \? 'true' : undefined"/)
  assert.match(itemTemplate, /<Loader2 v-if="item\.busy"/)
  assert.match(component, /if \(!item\.disabled && !item\.busy\) indexes\.push\(index\)/)
  assert.match(component, /if \(!item \|\| unavailable\.value \|\| item\.disabled \|\| item\.busy\) return/)
})

test('ActionMenu keyboard paths activate, wrap, restore focus, and dismiss', () => {
  const triggerHandler = sourceBlock(component, 'function handleTriggerKeydown', 'function handleMenuKeydown')
  const menuHandler = sourceBlock(component, 'function handleMenuKeydown', 'function closeFromOutsidePointer')

  assert.match(triggerHandler, /event\.key === 'ArrowDown' \|\| event\.key === 'Enter' \|\| event\.key === ' '/)
  assert.match(triggerHandler, /event\.key === 'ArrowUp'/)
  assert.match(triggerHandler, /openMenu\(firstSelectableIndex\(\)\)/)
  assert.match(triggerHandler, /openMenu\(lastSelectableIndex\(\)\)/)
  assert.match(menuHandler, /event\.key === 'Home'/)
  assert.match(menuHandler, /event\.key === 'End'/)
  assert.match(menuHandler, /event\.key === 'ArrowDown'/)
  assert.match(menuHandler, /event\.key === 'ArrowUp'/)
  assert.match(menuHandler, /focusItem\(firstSelectableIndex\(\)\)/)
  assert.match(menuHandler, /focusItem\(lastSelectableIndex\(\)\)/)
  assert.match(menuHandler, /moveActive\(1\)/)
  assert.match(menuHandler, /moveActive\(-1\)/)
  assert.match(component, /const selectableIndexes = computed\(\(\) => props\.items\.reduce<number\[\]>/)
  assert.match(component, /const count = props\.items\.length[\s\S]*?\(index \+ direction \+ count\) % count/)

  assert.match(menuHandler, /event\.key === 'Enter' \|\| event\.key === ' ' \|\| event\.key === 'Spacebar'/)
  assert.match(menuHandler, /event\.preventDefault\(\)[\s\S]*?selectActive\(\)/)
  assert.match(component, /function selectActive\(\): void \{[\s\S]*?select\(item\.id\)/)
  assert.match(component, /function select\(id: string\): void \{[\s\S]*?closeMenu\(true\)[\s\S]*?emit\('select', id\)/)
  assert.match(component, /if \(restoreFocus\) void nextTick\(\(\) => trigger\.value\?\.focus\(\)\)/)

  assert.match(menuHandler, /if \(event\.key === 'Escape'\)[\s\S]*?closeMenu\(true\)/)
  assert.match(triggerHandler, /if \(event\.key === 'Escape'\)[\s\S]*?closeMenu\(true\)/)
  assert.match(triggerHandler, /if \(event\.key === 'Tab'\)[\s\S]*?closeMenuAfterTab\(\)/)
  assert.match(menuHandler, /if \(event\.key === 'Tab'\)[\s\S]*?closeMenuAfterTab\(\)/)
  assert.match(component, /function closeMenuAfterTab\(\)[\s\S]*?closeMenu\(\)[\s\S]*?trigger\.value\?\.focus\(\)/)
  assert.doesNotMatch(component, /deferredCloseTimer/)
})

test('teleported LayoutSelector keeps native Tab navigation relative to its trigger', () => {
  assert.match(layoutSelector, /<Teleport to="body">/)
  assert.match(layoutSelector, /ref="panelRef"/)
  assert.match(layoutSelector, /@keydown="handleKeydown"/)
  assert.match(layoutSelector, /function closeMenuAfterTab\(\)[\s\S]*?closeMenu\(\)[\s\S]*?trigger\.value\?\.focus\(\)/)
  assert.doesNotMatch(layoutSelector, /deferredCloseTimer/)
})

test('ActionMenu exposes the trigger/menu ARIA relationship and outside dismissal guards', () => {
  const triggerTemplate = templateBlock(component, '<button\n      :id="triggerID"')
  assert.match(triggerTemplate, /:aria-label="accessibleLabel"/)
  assert.match(triggerTemplate, /:aria-controls="menuID"/)
  assert.match(triggerTemplate, /aria-haspopup="menu"/)
  assert.match(triggerTemplate, /:aria-expanded="open"/)
  assert.match(triggerTemplate, /:disabled="unavailable"/)

  const menuTemplate = sourceBlock(component, '<div\n        v-if="open"', '        <template v-for="(item, index)')
  assert.match(menuTemplate, /role="menu"/)
  assert.match(menuTemplate, /ref="panelRef"/)
  assert.match(menuTemplate, /:aria-label="label"/)
  assert.match(menuTemplate, /:aria-labelledby="triggerID"/)
  assert.match(component, /role="menuitem"/)
  assert.match(component, /:tabindex="index === activeIndex && isSelectable\(index\) \? 0 : -1"/)

  assert.match(component, /document\.addEventListener\('pointerdown', closeFromOutsidePointer, true\)/)
  assert.match(component, /document\.addEventListener\('focusin', closeFromOutsideFocus\)/)
  assert.match(component, /<Teleport to="body">/)
  assert.match(component, /panelRef\.value\?\.contains\(target\)/)
  for (const handler of ['closeFromOutsidePointer', 'closeFromOutsideFocus']) {
    const block = sourceBlock(component, `function ${handler}`, 'function focusTrigger')
    assert.match(block, /if \(!open\.value \|\| \(target && \(root\.value\?\.contains\(target\) \|\| panelRef\.value\?\.contains\(target\)\)\)\) return/)
    assert.match(block, /closeMenu\(\)/)
  }
})

test('caller busy state closes the menu, blocks repeat actions, and recovers', async () => {
  const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const executable = ts.transpileModule(script.replace(/^import .*$/gm, '').replace(/^export /gm, ''), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
  const props = reactive({ label: 'Actions for automation', items: [{ id: 'issue', label: 'Issue token' }], busyLabel: 'Issuing token for automation…' })
  const emitted = []
  const scope = effectScope()
  const api = scope.run(() => runInNewContext(`${executable}\n({ open, openMenu, select, accessibleLabel, unavailable })`, {
    computed, nextTick, ref, watch,
    defineProps: () => props,
    withDefaults: (value, defaults) => Object.assign(value, { ...defaults, ...value }),
    defineEmits: () => (...event) => emitted.push(event),
    defineExpose: () => {}, useId: () => 'test',
    onMounted: () => {}, onBeforeUnmount: () => {}, ensureRailgridUIStyles: () => {},
    useAnchoredPopover: () => {
      const open = ref(false)
      return { open, triggerRef: ref(null), panelRef: ref(null), panelStyle: ref({}), close: () => { open.value = false } }
    },
  }))
  try {
    api.openMenu()
    assert.equal(api.open.value, true)
    props.busy = true
    // Even before the watcher closes the panel, a second selection is denied.
    api.select('issue')
    assert.deepEqual(emitted, [])
    await nextTick()
    assert.equal(api.open.value, false)
    assert.equal(api.unavailable.value, true)
    assert.equal(api.accessibleLabel.value, 'Issuing token for automation…')
    api.openMenu()
    assert.equal(api.open.value, false)
    props.busyLabel = undefined
    assert.equal(api.accessibleLabel.value, 'Actions for automation…')
    props.busy = false
    await nextTick()
    assert.equal(api.accessibleLabel.value, 'Actions for automation')
    api.openMenu()
    api.select('issue')
    assert.deepEqual(emitted, [['select', 'issue']])
    assert.equal(api.open.value, false)
  } finally {
    scope.stop()
  }
})

test('busy progress stays visible outside the closed menu and is announced', () => {
  const trigger = templateBlock(component, '<button\n      :id="triggerID"')
  assert.match(trigger, /'k-table-action--busy': busy/)
  assert.match(trigger, /'k-table-action--neutral': busy/)
  assert.match(trigger, /<Loader2 v-if="busy" class="k-action-menu__busy"/)
  assert.match(trigger, /:aria-busy="busy \|\| undefined"/)
  assert.match(trigger, /:data-k-tip="accessibleLabel"/)
  const beforeMenu = sourceBlock(component, '</button>', '<Teleport to="body">')
  assert.match(beforeMenu, /role="status" aria-live="polite" aria-atomic="true">\{\{ busy \? accessibleLabel : '' \}\}/)
  assert.match(stylesheet, /\.k-table__primary-actions:has\(\.k-table-action--busy\)/)
})

test('canonical icon action, layer, and bounded search recipes remain intact', () => {
  assert.match(stylesheet, /\.k-icon-action\s*\{[\s\S]*?height: 32px;[\s\S]*?width: 32px;/)
  assert.match(stylesheet, /\.k-icon-action:focus-visible\s*\{[\s\S]*?outline: 2px solid var\(--color-accent/)
  assert.match(stylesheet, /@media \(pointer: coarse\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-icon-action\s*\{[\s\S]*?height: 44px;[\s\S]*?width: 44px;/)
  const layers = Object.fromEntries([...stylesheet.matchAll(/--k-layer-(menu|fullscreen|modal|toast):\s*(\d+)/g)].map(match => [match[1], Number(match[2])]))
  assert.deepEqual(Object.keys(layers).sort(), ['fullscreen', 'menu', 'modal', 'toast'])
  assert.ok(layers.menu < layers.fullscreen && layers.fullscreen < layers.modal && layers.modal < layers.toast)
  const searchShell = stylesheet.match(/\.k-table__search\s*\{[^}]*\}/)?.[0]
  const searchInput = stylesheet.match(/\.k-table__search-input\s*\{[^}]*\}/)?.[0]
  assert.ok(searchShell)
  assert.ok(searchInput)
  assert.match(searchShell, /max-width: 32rem;/)
  assert.doesNotMatch(searchInput, /max-width:/)
  assert.match(resourceTable, /class="k-table__scroll"/)

  const tableShell = stylesheet.match(/\.k-table\.k-table--resource\s*\{[\s\S]*?\n\}/)?.[0]
  assert.ok(tableShell)
  assert.match(tableShell, /max-width: 100%/)
  assert.match(tableShell, /width: 100%/)
  assert.doesNotMatch(tableShell, /max-width: 42rem/)
  assert.match(stylesheet, /\.k-table__scroll\s*\{[\s\S]*?max-width: 100%;[\s\S]*?overflow-x: auto;/)
})

test('ActionMenu geometry survives broad provider descendant constraints', () => {
  const menuRule = stylesheet.match(/\.k-action-menu\s*>\s*\.k-action-menu__menu\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(menuRule, /max-width:\s*calc\(100vw - 16px\)/)
  assert.match(menuRule, /min-width:\s*180px/)

  const tooltipRule = stylesheet.match(/\.k-action-menu__trigger\[data-k-tip\]::after\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(tooltipRule, /inset-inline-start:\s*auto/)
  assert.match(tooltipRule, /inset-inline-end:\s*0/)
  assert.match(tooltipRule, /transform:\s*none/)
  assert.match(tooltipRule, /max-width:\s*min\(260px, calc\(100vw - 16px\)\)/)
  assert.match(tooltipRule, /width:\s*max-content/)

  const expandedTooltipRule = stylesheet.match(/\.k-action-menu__trigger\[aria-expanded="true"\]\[data-k-tip\]::after\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(expandedTooltipRule, /opacity:\s*0/)
  assert.match(expandedTooltipRule, /visibility:\s*hidden/)
})

test('standalone style recovery distinguishes a stale host stylesheet', () => {
  assert.match(styles, /export const RAILGRID_UI_CORE_VERSION_MARKER = '--railgrid-ui-core-version'/)
  assert.equal(runtimeVersion, cssVersion, 'style handoff and canonical CSS must use the same version')
  assert.match(styles, /getPropertyValue\(RAILGRID_UI_CANONICAL_MARKER\)\.trim\(\) === RAILGRID_UI_CANONICAL_VALUE/)
  assert.match(styles, /function hasRequiredVersion\(value: string\): boolean/)
  assert.match(styles, /Number\.isFinite\(version\) && version >= RAILGRID_UI_CORE_VERSION/)
  assert.match(styles, /hasRequiredVersion\(styles\.getPropertyValue\(RAILGRID_UI_CORE_VERSION_MARKER\)\)/)
  assert.doesNotMatch(styles, /if \(document\.getElementById\(RAILGRID_UI_STYLE_ID\) \|\| hostStylesAreLoaded\(\)\) return/)
  assert.match(styles, /const fallbackStyleID = document\.getElementById\(RAILGRID_UI_STYLE_ID\)/)
  assert.match(styles, /`\$\{RAILGRID_UI_STYLE_ID\}-v\$\{RAILGRID_UI_CORE_VERSION\}`/)
  assert.match(styles, /style\.setAttribute\('data-railgrid-ui-core-version', String\(RAILGRID_UI_CORE_VERSION\)\)/)
})

test('standalone style recovery executes the stale/current/newer host matrix', () => {
  const staleHost = styleNode('k-railgrid-ui', 'stale-host-css')
  const stale = executableStylesHelper({ existingNodes: [staleHost] })
  stale.ensureRailgridUIStyles()
  assert.equal(stale.document.head.children.length, 1)
  assert.equal(stale.document.head.children[0].id, `k-railgrid-ui-v${cssVersion}`)
  assert.equal(stale.document.head.children[0].textContent, 'current-railgrid-ui')
  assert.equal(stale.document.head.children[0].getAttribute('data-railgrid-ui-core-version'), cssVersion)
  assert.equal(staleHost.textContent, 'stale-host-css')
  assert.equal(staleHost.getAttribute('data-railgrid-ui-version'), null)
  stale.ensureRailgridUIStyles()
  assert.equal(stale.document.head.children.length, 1)

  const current = executableStylesHelper({ version: cssVersion })
  current.ensureRailgridUIStyles()
  assert.equal(current.document.head.children.length, 0)

  const newerHost = executableStylesHelper({ version: String(Number(cssVersion) + 1), existingNodes: [styleNode('k-railgrid-ui', 'future-host-css')] })
  newerHost.ensureRailgridUIStyles()
  assert.equal(newerHost.document.head.children.length, 0)
  assert.equal(newerHost.document.getElementById('k-railgrid-ui').textContent, 'future-host-css')
})
