import assert from 'node:assert/strict'
import { readFile, rm } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { createRenderer, h, markRaw, nextTick, reactive, toRaw } from 'vue'

let AIModelReasoningPicker
const testDirectory = dirname(fileURLToPath(import.meta.url))
const componentPath = join(testDirectory, 'agentkit/AIModelReasoningPicker.vue')
const compiledPath = join(testDirectory, `.reasoning-picker-mounted-${process.pid}.cjs`)

test.before(async () => {
  const result = await build({
    entryPoints: [componentPath],
    bundle: true,
    external: ['vue', 'lucide-vue-next'],
    format: 'cjs',
    outfile: compiledPath,
    platform: 'node',
    plugins: [{
      name: 'compile-picker-vue',
      setup(pluginBuild) {
        pluginBuild.onResolve({ filter: /\.css\?inline$/ }, args => ({
          path: args.path,
          namespace: 'inline-css',
        }))
        pluginBuild.onLoad({ filter: /.*/, namespace: 'inline-css' }, () => ({
          contents: 'export default ""',
          loader: 'js',
        }))
        pluginBuild.onLoad({ filter: /\.vue$/ }, async args => {
          const source = await readFile(args.path, 'utf8')
          const { descriptor, errors } = parse(source, { filename: args.path })
          assert.deepEqual(errors, [], 'picker SFC parses')
          const compiled = compileScript(descriptor, {
            id: 'mounted-reasoning-picker',
            inlineTemplate: true,
          })
          return { contents: compiled.content, loader: 'ts', resolveDir: dirname(args.path) }
        })
      },
    }],
  })
  assert.equal(result.errors.length, 0)
  const require = createRequire(import.meta.url)
  ;({ default: AIModelReasoningPicker } = require(compiledPath))
})

test.after(async () => rm(compiledPath, { force: true }))

function makeHostNode(type, text = '') {
  const node = markRaw({
    type,
    text,
    value: '',
    scrollTop: 0,
    props: {},
    children: [],
    parent: null,
    style: { getPropertyValue: () => '' },
    getBoundingClientRect() {
      if (this.props.role === 'listbox') return { left: 24, right: 224, top: 48, bottom: 96, width: 200, height: 48 }
      if (this.props.role === 'option') {
        const index = Number(this.props.id?.split('-option-').at(-1) ?? 0)
        const top = 48 + index * 40
        return { left: 24, right: 224, top, bottom: top + 32, width: 200, height: 32 }
      }
      return { left: 24, right: 224, top: 48, bottom: 80, width: 200, height: 32 }
    },
    getClientRects: () => [{ width: 200, height: 32 }],
    getAttribute(name) { return this.props[name] ?? null },
    setAttribute(name, value) { this.props[name] = String(value) },
    get tabIndex() {
      if (this.props.tabindex !== undefined) return Number(this.props.tabindex)
      return ['button', 'input', 'select', 'textarea'].includes(this.type) ? 0 : -1
    },
    get clientTop() { return 0 },
    get clientHeight() { return this.props.role === 'listbox' ? 48 : 0 },
    contains(target) {
      return target === this || this.children.some(child => child.contains?.(target))
    },
    closest(selector) {
      if (selector === '[inert]' && this.props.inert !== undefined) return this
      return this.parent?.closest?.(selector) ?? null
    },
    focus() {
      if (this.props.disabled) return
      globalThis.document.activeElement = this
    },
    querySelector(selector) { return collect(toRaw(this)).find(child => matchesSelector(child, selector)) ?? null },
    querySelectorAll(selector) { return collect(toRaw(this)).filter(child => matchesSelector(child, selector)) },
    scrollIntoView() {},
  })
  return node
}

function collect(root) {
  const nodes = []
  const visit = node => {
    if (!node) return
    nodes.push(node)
    for (const child of node.children ?? []) visit(child)
  }
  visit(root)
  return nodes
}

function matchesSelector(node, selector) {
  return selector.split(',').some(part => {
    const simple = part.trim()
    const tag = simple.match(/^(button|input|select|textarea|a)(?:\[href\])?/)
    if (tag && node.type !== tag[1]) return false
    if (!tag && !simple.startsWith('[')) return false
    if (simple.includes(':not([disabled])') && node.props.disabled) return false
    if (simple.includes(':not([tabindex="-1"])') && node.tabIndex === -1) return false
    const positive = simple.replace(/:not\(\[[^\]]+\]\)/g, '')
    for (const [, name, expected] of positive.matchAll(/\[([\w-]+)(?:="([^"]*)")?\]/g)) {
      if (!Object.hasOwn(node.props, name)) return false
      if (expected !== undefined && node.getAttribute(name) !== expected) return false
    }
    if (tag?.[1] === 'a' && simple.includes('[href]') && !Object.hasOwn(node.props, 'href')) return false
    return true
  })
}

function installBrowserMocks() {
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  const previousNode = globalThis.Node
  class HostNode {}
  const body = makeHostNode('body')
  const root = makeHostNode('root')
  const listeners = new Map()
  const documentMock = {
    activeElement: null,
    body,
    documentElement: { style: { getPropertyValue: () => '' } },
    head: { appendChild() {} },
    createElement(type) { return makeHostNode(type) },
    getElementById() { return null },
    querySelector(selector) { return selector === 'body' ? body : null },
    querySelectorAll(selector) {
      return [...collect(root), ...collect(body)].filter(node => matchesSelector(node, selector))
    },
    addEventListener(type, listener) {
      const entries = listeners.get(type) ?? new Set()
      entries.add(listener)
      listeners.set(type, entries)
    },
    removeEventListener(type, listener) { listeners.get(type)?.delete(listener) },
  }
  const windowMock = {
    innerWidth: 1024,
    innerHeight: 768,
    getComputedStyle() { return { getPropertyValue: () => '' } },
    addEventListener() {},
    removeEventListener() {},
  }
  globalThis.document = documentMock
  globalThis.window = windowMock
  globalThis.Node = HostNode

  return {
    root,
    body,
    document: documentMock,
    restore() {
      if (previousDocument === undefined) delete globalThis.document
      else globalThis.document = previousDocument
      if (previousWindow === undefined) delete globalThis.window
      else globalThis.window = previousWindow
      if (previousNode === undefined) delete globalThis.Node
      else globalThis.Node = previousNode
    },
  }
}

function createPickerRenderer(body) {
  return createRenderer({
    createElement: type => makeHostNode(type),
    createText: text => makeHostNode('#text', text),
    createComment: text => makeHostNode('#comment', text),
    setText(node, text) { node.text = text },
    setElementText(node, text) {
      node.text = text
      node.children = []
    },
    patchProp(node, key, _previous, value) {
      node.props[key] = value
      if (key === 'value') node.value = String(value ?? '')
    },
    insert(node, parent, anchor = null) {
      if (node.parent) {
        const previousIndex = node.parent.children.indexOf(node)
        if (previousIndex >= 0) node.parent.children.splice(previousIndex, 1)
      }
      node.parent = parent
      const index = anchor ? parent.children.indexOf(anchor) : -1
      if (index < 0) parent.children.push(node)
      else parent.children.splice(index, 0, node)
    },
    remove(node) {
      if (!node.parent) return
      const index = node.parent.children.indexOf(node)
      if (index >= 0) node.parent.children.splice(index, 1)
      node.parent = null
    },
    parentNode: node => node.parent,
    nextSibling(node) {
      if (!node.parent) return null
      return node.parent.children[node.parent.children.indexOf(node) + 1] ?? null
    },
    querySelector: selector => selector === 'body' ? body : null,
    insertStaticContent(content, parent, anchor) {
      const node = makeHostNode('#static', content)
      node.parent = parent
      const index = anchor ? parent.children.indexOf(anchor) : -1
      if (index < 0) parent.children.push(node)
      else parent.children.splice(index, 0, node)
      return [node, node]
    },
  })
}

function textContent(node) {
  return [node.text ?? '', ...(node.children ?? []).map(textContent)].join(' ')
}

function findByTestID(roots, testID) {
  return roots.flatMap(collect).find(node => node.props?.['data-testid'] === testID)
}

function findButtonContaining(roots, label) {
  return roots.flatMap(collect).find(node => node.type === 'button' && textContent(node).includes(label))
}

function eventProp(node, name) {
  const wanted = `on${name}`.toLowerCase()
  return Object.keys(node.props).find(key => key.toLowerCase() === wanted)
}

async function dispatch(node, name, event = {}) {
  const key = eventProp(node, name)
  assert.ok(key, `${node.type} has ${name} handler`)
  const handler = node.props[key]
  const payload = {
    currentTarget: node,
    target: event.target ?? node,
    preventDefault() { payload.defaultPrevented = true },
    stopPropagation() {},
    defaultPrevented: false,
    ...event,
  }
  for (const callback of Array.isArray(handler) ? handler : [handler]) callback(payload)
  await nextTick()
  return payload
}

function mountPicker(browser) {
  const models = [
    { id: 'gpt-6-luna', name: 'GPT 6 Luna', model: 'gpt-6-luna', default: true },
    { id: 'o4-mini', name: 'O4 Mini', model: 'o4-mini' },
    { id: 'claude-sonnet', name: 'Claude Sonnet', model: 'claude-sonnet' },
  ]
  const optionsByModel = {
    'gpt-6-luna': ['none', 'low', 'medium', 'high', 'xhigh', 'max'].map(value => ({ value, label: value === 'xhigh' ? 'Extra high' : value[0].toUpperCase() + value.slice(1) })),
    'o4-mini': ['low', 'medium', 'high'].map(value => ({ value, label: value[0].toUpperCase() + value.slice(1) })),
    'claude-sonnet': ['low', 'medium'].map(value => ({ value, label: value[0].toUpperCase() + value.slice(1) })),
  }
  const defaultLabels = { 'gpt-6-luna': 'Medium', 'o4-mini': 'Low', 'claude-sonnet': '' }
  const state = reactive({
    selectedID: 'gpt-6-luna',
    effortByModel: { 'gpt-6-luna': 'none', 'o4-mini': 'high', 'claude-sonnet': 'medium' },
    disabled: false,
    reasoningDisabled: false,
    saving: false,
    validationError: '',
    saveError: '',
    resetRequired: false,
    editModelRequired: false,
  })
  const events = { efforts: [], retries: 0, settingsEdits: 0 }
  const renderer = createPickerRenderer(browser.body)
  const app = renderer.createApp({
    setup() {
      return () => h(AIModelReasoningPicker, {
        models,
        selectedID: state.selectedID,
        effort: state.effortByModel[state.selectedID],
        effortOptions: optionsByModel[state.selectedID],
        defaultEffortLabel: defaultLabels[state.selectedID],
        disabled: state.disabled,
        reasoningDisabled: state.reasoningDisabled,
        saving: state.saving,
        validationError: state.validationError,
        saveError: state.saveError,
        resetRequired: state.resetRequired,
        editModelRequired: state.editModelRequired,
        onSelect(modelID) { state.selectedID = modelID },
        'onUpdate:effort'(effort) {
          events.efforts.push(effort)
          state.effortByModel[state.selectedID] = effort
        },
        onRetry() { events.retries += 1 },
        onEditModelSettings() { events.settingsEdits += 1 },
      })
    },
  })
  app.mount(browser.root)
  return { app, state, events, roots: [browser.root, browser.body] }
}

test('mounted picker commits only declared choices, restores model drafts, and handles keyboard and recovery states', async () => {
  const browser = installBrowserMocks()
  const { app, state, events, roots } = mountPicker(browser)
  const nextOutside = makeHostNode('button')
  nextOutside.text = 'After picker'
  nextOutside.parent = browser.root
  browser.root.children.push(nextOutside)
  try {
    const trigger = findByTestID(roots, 'model-reasoning-trigger')
    assert.ok(trigger)
    assert.match(textContent(trigger), /GPT 6 Luna/)
    assert.match(textContent(trigger), /None/)

    await dispatch(trigger, 'click')
    let panel = findByTestID(roots, 'reasoning-picker-panel')
    assert.ok(panel)
    let slider = findByTestID(roots, 'reasoning-effort-slider')
    assert.equal(browser.body.contains(slider), true)
    assert.equal(
      browser.document.activeElement?.props?.['data-testid'],
      'reasoning-reset',
      `opening focuses the first recovery control (got ${browser.document.activeElement?.type}, ${browser.document.activeElement?.props?.['data-testid']})`,
    )

    await dispatch(slider, 'pointerdown')
    slider.value = '999'
    await dispatch(slider, 'input')
    assert.deepEqual(events.efforts, [], 'drag preview does not persist each pointer update')
    await dispatch(slider, 'change')
    assert.deepEqual(events.efforts, ['max'], 'out-of-range slider input clamps to the last declared effort')
    assert.equal(state.effortByModel['gpt-6-luna'], 'max')

    const modelButton = findByTestID(roots, 'reasoning-model-trigger')
    assert.ok(modelButton)
    await dispatch(modelButton, 'click')
    let modelList = findByTestID(roots, 'reasoning-model-list')
    assert.ok(modelList)
    assert.equal(browser.document.activeElement?.props?.['data-testid'], 'reasoning-model-list')
    await dispatch(modelList, 'keydown', { key: 'ArrowDown' })
    assert.match(modelList.props['aria-activedescendant'], /option-1$/)
    await dispatch(modelList, 'keydown', { key: 'Enter' })
    assert.equal(state.selectedID, 'o4-mini')
    assert.equal(state.effortByModel['o4-mini'], 'high')
    assert.ok(findByTestID(roots, 'reasoning-picker-panel'), 'model selection keeps the popover open')
    let currentTrigger = findByTestID(roots, 'model-reasoning-trigger')
    assert.match(textContent(currentTrigger), /O4 Mini/)
    assert.match(textContent(currentTrigger), /High/)

    const reset = findByTestID(roots, 'reasoning-reset')
    assert.ok(reset)
    await dispatch(reset, 'click')
    assert.equal(events.efforts.at(-1), '')
    assert.equal(state.effortByModel['o4-mini'], '')
    assert.match(textContent(currentTrigger), /Model default/)

    const currentModelButton = findByTestID(roots, 'reasoning-model-trigger')
    await dispatch(currentModelButton, 'click')
    modelList = findByTestID(roots, 'reasoning-model-list')
    await dispatch(modelList, 'keydown', { key: 'ArrowUp' })
    await dispatch(modelList, 'keydown', { key: 'Enter' })
    assert.equal(state.selectedID, 'gpt-6-luna')
    assert.equal(state.effortByModel['gpt-6-luna'], 'max', 'switching back restores only that model’s saved draft')
    currentTrigger = findByTestID(roots, 'model-reasoning-trigger')
    assert.match(textContent(currentTrigger), /Max/)

    await dispatch(findByTestID(roots, 'reasoning-model-trigger'), 'click')
    panel = findByTestID(roots, 'reasoning-picker-panel')
    modelList = findByTestID(roots, 'reasoning-model-list')
    await dispatch(modelList, 'keydown', { key: 'End' })
    assert.match(modelList.props['aria-activedescendant'], /option-2$/)
    assert.ok(modelList.scrollTop > 0, 'keyboard navigation scrolls the active model into view')
    const spaceSelection = await dispatch(modelList, 'keydown', { key: ' ' })
    assert.equal(spaceSelection.defaultPrevented, true)
    assert.equal(state.selectedID, 'claude-sonnet', 'Space activates the active model option')
    assert.equal(state.effortByModel['claude-sonnet'], 'medium')
    assert.match(textContent(findByTestID(roots, 'model-reasoning-trigger')), /Medium/)

    const newModelButton = findByTestID(roots, 'reasoning-model-trigger')
    await dispatch(newModelButton, 'click')
    modelList = findByTestID(roots, 'reasoning-model-list')
    await dispatch(findByTestID(roots, 'reasoning-picker-panel'), 'keydown', { key: 'Escape', target: modelList })
    assert.ok(findByTestID(roots, 'reasoning-picker-panel'), 'Escape from model list returns to reasoning controls')
    assert.equal(browser.document.activeElement?.props?.['data-testid'], 'reasoning-model-trigger')

    // Tab stays in the dialog until it reaches the last control, then returns
    // focus to the next control in the host document.
    const activePanel = findByTestID(roots, 'reasoning-picker-panel')
    const firstControl = findByTestID(roots, 'reasoning-model-trigger')
    const internalTab = await dispatch(activePanel, 'keydown', { key: 'Tab', target: firstControl })
    assert.equal(internalTab.defaultPrevented, false)
    assert.ok(findByTestID(roots, 'reasoning-picker-panel'))
    slider = findByTestID(roots, 'reasoning-effort-slider')
    const boundaryTab = await dispatch(activePanel, 'keydown', { key: 'Tab', target: slider })
    assert.equal(boundaryTab.defaultPrevented, true)
    assert.equal(findByTestID(roots, 'reasoning-picker-panel'), undefined)
    assert.equal(browser.document.activeElement, nextOutside, 'Tab at the panel boundary exits to the next host control')

    currentTrigger = findByTestID(roots, 'model-reasoning-trigger')
    await dispatch(currentTrigger, 'click')
    panel = findByTestID(roots, 'reasoning-picker-panel')
    await dispatch(panel, 'keydown', { key: 'Escape' })
    assert.equal(findByTestID(roots, 'reasoning-picker-panel'), undefined)
    assert.equal(
      browser.document.activeElement?.props?.['data-testid'],
      'model-reasoning-trigger',
      'Escape closes the dialog and restores trigger focus',
    )

    state.effortByModel['gpt-6-luna'] = 'retired'
    state.resetRequired = true
    state.validationError = 'The saved reasoning effort is no longer supported.'
    state.editModelRequired = true
    await nextTick()
    const recoveryReset = findByTestID(roots, 'reasoning-reset')
    assert.ok(recoveryReset)
    await dispatch(recoveryReset, 'click')
    assert.equal(events.efforts.at(-1), '', 'stale values reset to the model default with the empty sentinel')

    state.saveError = 'Could not save this preference.'
    await nextTick()
    assert.match(textContent(browser.root), /Could not save this preference/)
    await dispatch(findButtonContaining(roots, 'Retry'), 'click')
    assert.equal(events.retries, 1)
    await dispatch(findButtonContaining(roots, 'Edit model settings'), 'click')
    assert.equal(events.settingsEdits, 1)

    currentTrigger = findByTestID(roots, 'model-reasoning-trigger')
    await dispatch(currentTrigger, 'click')
    slider = findByTestID(roots, 'reasoning-effort-slider')
    state.saving = true
    await nextTick()
    assert.equal(slider.props['aria-disabled'], 'true')
    assert.match(textContent(browser.body), /Saving preference/)
    const effortEventCount = events.efforts.length
    const keyboardLock = await dispatch(slider, 'keydown', { key: 'ArrowRight' })
    assert.equal(keyboardLock.defaultPrevented, true, 'saving locks slider keyboard changes')
    const pointerLock = await dispatch(slider, 'pointerdown')
    assert.equal(pointerLock.defaultPrevented, true, 'saving blocks slider pointer gestures')
    slider.value = '0'
    await dispatch(slider, 'input')
    await dispatch(slider, 'change')
    assert.equal(events.efforts.length, effortEventCount, 'saving prevents a preference update')

    await dispatch(findByTestID(roots, 'reasoning-picker-panel'), 'keydown', { key: 'Escape' })
    assert.equal(findByTestID(roots, 'reasoning-picker-panel'), undefined)
    await dispatch(findByTestID(roots, 'model-reasoning-trigger'), 'click')
    assert.equal(
      browser.document.activeElement?.props?.['data-testid'],
      'reasoning-model-trigger',
      'opening while saving skips the disabled reset and focuses the enabled model control',
    )
    const modelControlDuringSave = findByTestID(roots, 'reasoning-model-trigger')
    await dispatch(modelControlDuringSave, 'keydown', { key: 'ArrowDown' })
    const modelListDuringSave = findByTestID(roots, 'reasoning-model-list')
    assert.ok(modelListDuringSave)
    assert.equal(browser.document.activeElement?.props?.['data-testid'], 'reasoning-model-list')
    await dispatch(modelListDuringSave, 'keydown', { key: 'Home' })
    await dispatch(modelListDuringSave, 'keydown', { key: 'Enter' })
    assert.equal(state.selectedID, 'gpt-6-luna', 'keyboard model selection remains available while reasoning is saving')
    assert.equal(events.efforts.length, effortEventCount, 'model selection while saving does not write a reasoning effort')

    state.disabled = true
    await nextTick()
    assert.equal(findByTestID(roots, 'model-reasoning-trigger').props.disabled, true)
    assert.equal(findByTestID(roots, 'reasoning-picker-panel'), undefined)
  } finally {
    app.unmount()
    await nextTick()
    browser.restore()
  }
})
