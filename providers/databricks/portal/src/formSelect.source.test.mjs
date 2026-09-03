import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { createRenderer, h, markRaw, nextTick } from 'vue'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'

const read = path => readFile(new URL(path, import.meta.url), 'utf8')
const testDirectory = dirname(fileURLToPath(import.meta.url))
const portalRoot = resolve(testDirectory, '..')
const repositoryRoot = resolve(portalRoot, '../../..')
const canonicalFormSelect = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/FormSelect.vue')
const vendoredFormSelect = resolve(portalRoot, 'src/portalkit/FormSelect.vue')
let vite

test.before(async () => {
  vite = await createServer({
    root: portalRoot,
    appType: 'custom',
    cacheDir: '/tmp/faros-databricks-form-select-vite',
    configFile: false,
    plugins: [vue()],
    resolve: {
      dedupe: ['vue'],
      alias: {
        'lucide-vue-next': resolve(portalRoot, 'node_modules/lucide-vue-next'),
      },
    },
    server: { middlewareMode: true, hmr: false, fs: { allow: [repositoryRoot] } },
  })
})

test.after(async () => vite?.close())

async function loadFormSelect() {
  const module = await vite.ssrLoadModule('/src/portalkit/FormSelect.vue')
  const source = await readFile(vendoredFormSelect, 'utf8')
  const template = source.match(/<template(?:\s[^>]*)?>([\s\S]*)<\/template>/)?.[1]
  assert.ok(template, 'FormSelect has a template for mounted behavior coverage')
  const [{ compile }, vueRuntime] = await Promise.all([
    import('@vue/compiler-dom'),
    import('vue'),
  ])
  const compiled = compile(template, { mode: 'function', prefixIdentifiers: true, expressionPlugins: ['typescript'] })
  assert.equal((compiled.errors ?? []).length, 0, 'FormSelect template compiles for mounted behavior coverage')
  const render = new Function('Vue', compiled.code)(vueRuntime)
  module.default.render = (ctx, cache, _props, setupState) => render(new Proxy(ctx, {
    get(target, key, receiver) {
      if (typeof key === 'string' && Object.prototype.hasOwnProperty.call(setupState, key)) return setupState[key]
      return Reflect.get(target, key, receiver)
    },
  }), cache)
  return module.default
}

function createInteractionHarness() {
  const documentListeners = new Map()
  const windowListeners = new Map()
  let document

  const textNode = (text, parent = null) => markRaw({ type: '#text', text, parent })
  const createNode = type => {
    const node = {
      type,
      props: {},
      children: [],
      parent: null,
      listeners: new Map(),
      addEventListener(name, handler) {
        if (!node.listeners.has(name)) node.listeners.set(name, new Set())
        node.listeners.get(name).add(handler)
      },
      removeEventListener(name, handler) {
        node.listeners.get(name)?.delete(handler)
      },
      dispatchEvent(event) {
        for (const handler of node.listeners.get(event.type) ?? []) handler(event)
      },
      contains(target) {
        for (let current = target; current; current = current.parent) {
          if (current === node) return true
        }
        return false
      },
      focus() {
        document.activeElement = node
      },
      getBoundingClientRect() {
        return { left: 100, top: 100, right: 340, bottom: 140, width: 240, height: 40 }
      },
      scrollIntoView() {},
    }
    node.removeAttribute = name => { delete node.props[name] }
    node.setAttribute = (name, value) => { node.props[name] = String(value) }
    node.getRootNode = () => document
    return markRaw(node)
  }

  const body = createNode('body')
  const root = createNode('#root')
  const findAll = (start, predicate, result = []) => {
    if (predicate(start)) result.push(start)
    for (const child of start.children ?? []) findAll(child, predicate, result)
    return result
  }
  document = {
    body,
    activeElement: null,
    documentElement: { clientWidth: 1200, clientHeight: 900 },
    head: { appendChild() {} },
    createElement: createNode,
    getElementById: id => findAll(root, node => node.props?.id === id).concat(findAll(body, node => node.props?.id === id))[0] ?? null,
    addEventListener(name, handler) {
      if (!documentListeners.has(name)) documentListeners.set(name, new Set())
      documentListeners.get(name).add(handler)
    },
    removeEventListener(name, handler) {
      documentListeners.get(name)?.delete(handler)
    },
  }
  const window = {
    innerWidth: 1200,
    innerHeight: 900,
    addEventListener(name, handler) {
      if (!windowListeners.has(name)) windowListeners.set(name, new Set())
      windowListeners.get(name).add(handler)
    },
    removeEventListener(name, handler) {
      windowListeners.get(name)?.delete(handler)
    },
  }
  const renderer = createRenderer({
    patchProp(node, key, _previous, value) {
      node.props[key] = value
    },
    insert(node, parent, anchor = null) {
      if (node.parent) {
        const oldIndex = node.parent.children.indexOf(node)
        if (oldIndex >= 0) node.parent.children.splice(oldIndex, 1)
      }
      node.parent = parent
      const index = anchor ? parent.children.indexOf(anchor) : -1
      parent.children.splice(index >= 0 ? index : parent.children.length, 0, node)
    },
    remove(node) {
      const index = node.parent?.children.indexOf(node) ?? -1
      if (index >= 0) node.parent.children.splice(index, 1)
      node.parent = null
    },
    createElement: createNode,
    createText: textNode,
    createComment: text => markRaw({ type: '#comment', text, parent: null }),
    setText(node, text) { node.text = text },
    setElementText(node, text) {
      node.children = [textNode(text, node)]
    },
    parentNode: node => node.parent,
    nextSibling(node) {
      const siblings = node.parent?.children ?? []
      const index = siblings.indexOf(node)
      return index >= 0 ? siblings[index + 1] ?? null : null
    },
    querySelector: selector => selector === 'body' ? body : null,
    setScopeId() {},
    cloneNode(node) {
      return markRaw({ ...node, props: { ...node.props }, children: [...node.children] })
    },
    insertStaticContent() {
      return [textNode(''), textNode('')]
    },
  })
  return {
    document,
    window,
    body,
    root,
    createNode,
    find: predicate => findAll(root, predicate).concat(findAll(body, predicate))[0] ?? null,
    findAll: predicate => findAll(root, predicate).concat(findAll(body, predicate)),
    dispatchDocument(name, event) {
      for (const handler of documentListeners.get(name) ?? []) handler(event)
    },
    renderer,
  }
}

function mountFormSelect(FormSelect, props) {
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  const harness = createInteractionHarness()
  const emitted = []
  globalThis.document = harness.document
  globalThis.window = harness.window
  const app = harness.renderer.createApp({
    render: () => h(FormSelect, {
      ...props,
      'onUpdate:modelValue': value => emitted.push(value),
    }),
  })
  const iconStub = { render: () => null }
  app.component('Check', iconStub)
  app.component('ChevronDown', iconStub)
  app._context.provides[Symbol.for('v-scx')] = { modules: new Set() }
  app.mount(harness.root)
  return {
    ...harness,
    emitted,
    instance: app._instance,
    unmount() {
      app.unmount()
      if (previousDocument === undefined) delete globalThis.document
      else globalThis.document = previousDocument
      if (previousWindow === undefined) delete globalThis.window
      else globalThis.window = previousWindow
    },
  }
}

async function flushVue() {
  for (let index = 0; index < 5; index += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function keyEvent(key) {
  return {
    key,
    defaultPrevented: false,
    preventDefault() { this.defaultPrevented = true },
  }
}

test('FormSelect exposes the accessible keyboard and option contract', async () => {
  const [source, canonicalSource] = await Promise.all([
    readFile(vendoredFormSelect, 'utf8'),
    readFile(canonicalFormSelect, 'utf8'),
  ])

  assert.equal(source, canonicalSource, 'Databricks uses the synced canonical FormSelect')
  assert.doesNotMatch(source, /databricks|faros-provider-databricks/, 'canonical FormSelect has no provider marker')

  for (const contract of [
    /export interface FormSelectOption/, /modelValue: string/, /options: readonly FormSelectOption\[\]/,
    /placeholder\?: string/, /disabled\?: boolean/, /required\?: boolean/, /invalid\?: boolean/,
    /describedby\?: string/, /labelledby\?: string/, /name\?: string/, /id\?: string/,
    /role="combobox"/, /aria-haspopup="listbox"/, /:aria-controls="listboxID"/, /:aria-expanded=/,
    /:aria-activedescendant="activeDescendant"/, /:aria-describedby="describedby/, /:aria-labelledby="accessibleLabelledby"/,
    /:aria-required="required/, /:aria-invalid="invalid/, /data-form-select-trigger/,
    /role="listbox"/, /role="option"/, /:aria-selected=/, /:aria-disabled=/, /:disabled="option.disabled"/,
    /class="k-form-select"/, /class="k-input k-form-select__trigger"/,
    /class="k-menu k-form-select__panel k-form-select__portal"/, /class="k-menu-item k-form-select__option"/, /class="k-form-select__check"/,
    /event\.key === 'ArrowDown'/, /event\.key === 'ArrowUp'/, /event\.key === 'Home'/, /event\.key === 'End'/,
    /event\.key === 'Enter'/, /event\.key === ' '/, /event\.key === 'Escape'/, /event\.key === 'Tab'/,
    /document\.addEventListener\('pointerdown'/, /document\.addEventListener\('focusin'/,
    /window\.addEventListener\('resize'/, /window\.addEventListener\('scroll'/,
    /position: 'fixed'/, /maxHeight: `\$\{availableHeight\}px`/, /defineExpose\(\{ focus \}\)/,
    /@mousedown\.prevent/, /closeSelect\(true\)/,
  ]) assert.match(source, contract)

  assert.match(source, /const accessibleLabelledby = computed\(\(\) => \[props\.labelledby, valueID\.value\]/)
  assert.match(source, /<span :id="valueID" class="k-form-select__value"/)
  assert.match(source, /<input[\s\S]*type="hidden"[\s\S]*:name="name"[\s\S]*:value="modelValue"/)
  assert.match(source, /function moveActive\(delta: 1 \| -1\)/)
  assert.match(source, /if \(isSelectable\(index\)\)/)
  assert.match(source, /if \(props\.disabled \|\| option\.disabled\) return/)
})

test('Databricks create and import surfaces use FormSelect without native selects', async () => {
  const sources = await Promise.all([
    read('./ResourceImportWizard.vue'),
    read('./views/CreateWarehouseView.vue'),
    read('./views/CreateTableView.vue'),
  ])

  for (const source of sources) {
    assert.doesNotMatch(source, /<select\b/)
    assert.match(source, /import FormSelect from ['"][^'"]*FormSelect\.vue['"]\s*/)
    assert.match(source, /<FormSelect[\s\S]*v-model=/)
    assert.match(source, /:options=/)
    assert.match(source, /:disabled=/)
    assert.match(source, /required/)
  }

  const [wizard, warehouse, table] = sources
  assert.match(wizard, /const matchingWarehouses = computed\([\s\S]*connectionRef\.value\)/)
  assert.match(wizard, /const warehouseOptions = computed\(\(\) => matchingWarehouses\.value\.map/)
  assert.match(wizard, /:options="warehouseOptions"/)
  assert.match(wizard, /\[data-form-select-trigger\]:not\(:disabled\)/)
  assert.match(warehouse, /:options="connectionOptions"/)
  assert.match(table, /ref="connectionInput"[\s\S]*v-model="form\.connectionRef"/)
  assert.match(table, /const connectionInput = ref<\{ focus: \(\) => void \}\ \| null>/)
  assert.match(table, /const warehouseOptions = computed\(\(\) => formWarehouses\.value\.map/)
})

test('FormSelect mounts its teleported listbox and preserves keyboard interaction semantics', { timeout: 15_000 }, async () => {
  const FormSelect = await loadFormSelect()
  const options = [
    { value: 'first', label: 'First' },
    { value: 'disabled', label: 'Disabled', disabled: true },
    { value: 'third', label: 'Third' },
  ]
  const mounted = mountFormSelect(FormSelect, {
    modelValue: '',
    options,
    id: 'runtime-select',
    labelledby: 'runtime-label',
    describedby: 'runtime-help',
  })

  try {
    const trigger = mounted.find(node => node.props?.['data-form-select-trigger'] !== undefined)
    assert.ok(trigger, 'FormSelect renders a trigger')
    assert.equal(trigger.props.role, 'combobox')
    assert.equal(trigger.props['aria-haspopup'], 'listbox')
    assert.equal(trigger.props['aria-expanded'], 'false')

    trigger.props.onClick({})
    await flushVue()
    let panel = mounted.find(node => node.props?.role === 'listbox')
    assert.ok(panel, 'click opens the listbox')
    assert.equal(panel.parent === mounted.body, true, 'listbox is teleported to body')
    assert.equal(panel.props.id, trigger.props['aria-controls'])
    assert.equal(panel.props['aria-labelledby'], trigger.props['aria-labelledby'])
    const optionNodes = mounted.findAll(node => node.props?.role === 'option')
    assert.equal(optionNodes.length, 3)
    assert.equal(optionNodes[1].props.disabled, true)
    assert.equal(optionNodes[1].props['aria-disabled'], 'true')
    assert.equal(trigger.props['aria-expanded'], 'true')
    assert.equal(trigger.props['aria-activedescendant'], optionNodes[0].props.id)

    const down = keyEvent('ArrowDown')
    trigger.props.onKeydown(down)
    await flushVue()
    assert.equal(down.defaultPrevented, true)
    assert.equal(trigger.props['aria-activedescendant'], optionNodes[2].props.id)
    const up = keyEvent('ArrowUp')
    trigger.props.onKeydown(up)
    await flushVue()
    assert.equal(trigger.props['aria-activedescendant'], optionNodes[0].props.id)

    trigger.props.onKeydown(keyEvent('ArrowDown'))
    trigger.props.onKeydown(keyEvent('Enter'))
    await flushVue()
    assert.deepEqual(mounted.emitted, ['third'], 'Enter emits the active value')
    assert.equal(mounted.find(node => node.props?.role === 'listbox') === null, true, 'Enter closes the listbox')
    assert.equal(mounted.document.activeElement === trigger, true, 'selection restores trigger focus')

    trigger.props.onKeydown(keyEvent('ArrowDown'))
    await flushVue()
    panel = mounted.find(node => node.props?.role === 'listbox')
    assert.ok(panel, 'ArrowDown opens a closed select')
    trigger.props.onKeydown(keyEvent('ArrowDown'))
    trigger.props.onKeydown(keyEvent(' '))
    await flushVue()
    assert.deepEqual(mounted.emitted, ['third', 'third'], 'Space emits the active value')
    assert.equal(mounted.find(node => node.props?.role === 'listbox') === null, true, 'Space closes the listbox')

    trigger.props.onClick({})
    await flushVue()
    const escape = keyEvent('Escape')
    trigger.props.onKeydown(escape)
    await flushVue()
    assert.equal(escape.defaultPrevented, true)
    assert.equal(mounted.find(node => node.props?.role === 'listbox') === null, true, 'Escape closes the listbox')
    assert.equal(mounted.document.activeElement === trigger, true, 'Escape restores trigger focus')

    trigger.props.onKeydown(keyEvent('ArrowDown'))
    await flushVue()
    assert.ok(mounted.find(node => node.props?.role === 'listbox'))
    mounted.dispatchDocument('pointerdown', { target: mounted.createNode('outside') })
    await flushVue()
    assert.equal(mounted.find(node => node.props?.role === 'listbox') === null, true, 'outside pointer closes the listbox')
  } finally {
    mounted.unmount()
  }

  const disabled = mountFormSelect(FormSelect, {
    modelValue: '',
    options,
    id: 'disabled-select',
    disabled: true,
  })
  try {
    const trigger = disabled.find(node => node.props?.['data-form-select-trigger'] !== undefined)
    trigger.props.onClick({})
    trigger.props.onKeydown(keyEvent('Enter'))
    await flushVue()
    assert.equal(trigger.props['aria-expanded'], 'false', 'disabled trigger does not open')
    assert.equal(disabled.find(node => node.props?.role === 'listbox') === null, true)
  } finally {
    disabled.unmount()
  }
})

test('FormSelect styling inherits k-menu language and stays viewport aware', async () => {
  const [canonicalStyles, providerStyles] = await Promise.all([
    readFile(resolve(repositoryRoot, 'provider-sdk/portalkit/faros-ui.css'), 'utf8'),
    read('./style.css'),
  ])
  assert.match(canonicalStyles, /\.k-form-select\s*\{[\s\S]*min-width: 0/)
  assert.match(canonicalStyles, /\.k-form-select__panel\.k-form-select__portal[\s\S]*position: fixed/)
  assert.match(canonicalStyles, /\.k-form-select__panel\.k-form-select__portal[\s\S]*overflow-y: auto/)
  assert.match(canonicalStyles, /\.k-form-select__panel\.k-form-select__portal[\s\S]*z-index: 1000/)
  assert.match(canonicalStyles, /\.k-form-select__portal \.k-form-select__option\.is-active:not\(:disabled\):not\(\.is-selected\)/)
  assert.match(canonicalStyles, /\.k-form-select__trigger[\s\S]*cursor: pointer/)
  assert.match(providerStyles, /\.manual-create-form \[data-form-select-trigger\][\s\S]*min-height: 44px/)
  assert.doesNotMatch(providerStyles, /\.k-[A-Za-z0-9_-]+/)
})
