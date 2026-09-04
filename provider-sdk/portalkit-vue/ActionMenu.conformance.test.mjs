import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

const component = readFileSync(new URL('./ActionMenu.vue', import.meta.url), 'utf8')
const stylesheet = readFileSync(new URL('../portalkit/faros-ui.css', import.meta.url), 'utf8')
const styles = readFileSync(new URL('../portalkit/styles.ts', import.meta.url), 'utf8')
const resourceTable = readFileSync(new URL('./ResourceTable.vue', import.meta.url), 'utf8')

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
    ['--faros-ui-canonical', canonical],
    ['--faros-ui-version', version],
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
        computedValues.set('--faros-ui-canonical', '1')
        computedValues.set('--faros-ui-version', node.getAttribute('data-faros-ui-version') ?? '')
        return node
      },
    },
  }
  const context = { document, window: { getComputedStyle: () => ({ getPropertyValue: name => computedValues.get(name) ?? '' }) } }
  const executable = styles
    .replace(/^import farosUIStyles.*$/m, "const farosUIStyles = 'current-faros-ui';")
    .replaceAll('export const ', 'const ')
    .replaceAll('export function ', 'function ')
    .replaceAll(': string', '')
    .replaceAll(': boolean', '')
    .replaceAll(': void', '')
    + '\n;globalThis.__styles = { ensureFarosUIStyles, FAROS_UI_STYLE_ID, FAROS_UI_VERSION };'
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

  const itemTemplate = templateBlock(component, '<button\n        v-for="(item, index)')
  assert.match(itemTemplate, /:class="item\.tone \? `k-menu-item--\$\{item\.tone\}` : undefined"/)
  assert.match(itemTemplate, /:disabled="item\.disabled \|\| item\.busy"/)
  assert.match(itemTemplate, /:aria-disabled="item\.disabled \|\| item\.busy \? 'true' : undefined"/)
  assert.match(itemTemplate, /:aria-busy="item\.busy \? 'true' : undefined"/)
  assert.match(itemTemplate, /<Loader2 v-if="item\.busy"/)
  assert.match(component, /if \(!item\.disabled && !item\.busy\) indexes\.push\(index\)/)
  assert.match(component, /if \(!item \|\| props\.disabled \|\| item\.disabled \|\| item\.busy\) return/)
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
  assert.match(component, /deferredCloseTimer = setTimeout\(\(\) => \{[\s\S]*?closeMenu\(\)[\s\S]*?\}, 0\)/)
})

test('ActionMenu exposes the trigger/menu ARIA relationship and outside dismissal guards', () => {
  const triggerTemplate = templateBlock(component, '<button\n      :id="triggerID"')
  assert.match(triggerTemplate, /:aria-label="label"/)
  assert.match(triggerTemplate, /:aria-controls="menuID"/)
  assert.match(triggerTemplate, /aria-haspopup="menu"/)
  assert.match(triggerTemplate, /:aria-expanded="open"/)
  assert.match(triggerTemplate, /:disabled="disabled"/)

  const menuTemplate = sourceBlock(component, '<div\n      v-if="open"', '      <button\n        v-for="(item, index)')
  assert.match(menuTemplate, /role="menu"/)
  assert.match(menuTemplate, /:aria-label="label"/)
  assert.match(menuTemplate, /:aria-labelledby="triggerID"/)
  assert.match(component, /role="menuitem"/)
  assert.match(component, /:tabindex="index === activeIndex && isSelectable\(index\) \? 0 : -1"/)

  assert.match(component, /document\.addEventListener\('pointerdown', closeFromOutsidePointer, true\)/)
  assert.match(component, /document\.addEventListener\('focusin', closeFromOutsideFocus\)/)
  for (const handler of ['closeFromOutsidePointer', 'closeFromOutsideFocus']) {
    const block = sourceBlock(component, `function ${handler}`, 'function focusTrigger')
    assert.match(block, /if \(!open\.value \|\| \(target && root\.value\?\.contains\(target\)\)\) return/)
    assert.match(block, /closeMenu\(\)/)
  }
})

test('canonical icon action, layer, and bounded search recipes remain intact', () => {
  assert.match(stylesheet, /\.k-icon-action\s*\{[\s\S]*?height: 32px;[\s\S]*?width: 32px;/)
  assert.match(stylesheet, /\.k-icon-action:focus-visible\s*\{[\s\S]*?outline: 2px solid var\(--color-accent/)
  assert.match(stylesheet, /@media \(pointer: coarse\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-icon-action\s*\{[\s\S]*?height: 44px;[\s\S]*?width: 44px;/)
  const layers = Object.fromEntries([...stylesheet.matchAll(/--k-layer-(menu|fullscreen|modal|toast):\s*(\d+)/g)].map(match => [match[1], Number(match[2])]))
  assert.deepEqual(Object.keys(layers).sort(), ['fullscreen', 'menu', 'modal', 'toast'])
  assert.ok(layers.menu < layers.fullscreen && layers.fullscreen < layers.modal && layers.modal < layers.toast)
  assert.match(stylesheet, /\.k-table__search\s*\{[\s\S]*?max-width: 42rem;/)
  assert.match(stylesheet, /\.k-table__search-input\s*\{[\s\S]*?max-width: 42rem;/)
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
  assert.match(styles, /export const FAROS_UI_VERSION_MARKER = '--faros-ui-version'/)
  assert.match(styles, /export const FAROS_UI_VERSION = 3/)
  assert.match(styles, /getPropertyValue\(FAROS_UI_CANONICAL_MARKER\)\.trim\(\) === FAROS_UI_CANONICAL_VALUE/)
  assert.match(styles, /function hasRequiredVersion\(value: string\): boolean/)
  assert.match(styles, /Number\.isFinite\(version\) && version >= FAROS_UI_VERSION/)
  assert.match(styles, /hasRequiredVersion\(styles\.getPropertyValue\(FAROS_UI_VERSION_MARKER\)\)/)
  assert.doesNotMatch(styles, /if \(document\.getElementById\(FAROS_UI_STYLE_ID\) \|\| hostStylesAreLoaded\(\)\) return/)
  assert.match(styles, /const fallbackStyleID = document\.getElementById\(FAROS_UI_STYLE_ID\)/)
  assert.match(styles, /`\$\{FAROS_UI_STYLE_ID\}-v\$\{FAROS_UI_VERSION\}`/)
  assert.match(styles, /style\.setAttribute\('data-faros-ui-version', String\(FAROS_UI_VERSION\)\)/)
})

test('standalone style recovery executes the stale/current/newer host matrix', () => {
  const staleHost = styleNode('k-faros-ui', 'stale-host-css')
  const stale = executableStylesHelper({ existingNodes: [staleHost] })
  stale.ensureFarosUIStyles()
  assert.equal(stale.document.head.children.length, 1)
  assert.equal(stale.document.head.children[0].id, 'k-faros-ui-v3')
  assert.equal(stale.document.head.children[0].textContent, 'current-faros-ui')
  assert.equal(stale.document.head.children[0].getAttribute('data-faros-ui-version'), '3')
  assert.equal(staleHost.textContent, 'stale-host-css')
  assert.equal(staleHost.getAttribute('data-faros-ui-version'), null)
  stale.ensureFarosUIStyles()
  assert.equal(stale.document.head.children.length, 1)

  const current = executableStylesHelper({ version: '3' })
  current.ensureFarosUIStyles()
  assert.equal(current.document.head.children.length, 0)

  const newerHost = executableStylesHelper({ version: '4', existingNodes: [styleNode('k-faros-ui', 'future-host-css')] })
  newerHost.ensureFarosUIStyles()
  assert.equal(newerHost.document.head.children.length, 0)
  assert.equal(newerHost.document.getElementById('k-faros-ui').textContent, 'future-host-css')
})
