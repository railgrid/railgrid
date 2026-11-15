import { afterEach, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h, nextTick, ref, ssrContextKey, type Component, type RendererOptions } from '@vue/runtime-core'
import * as VueRuntime from 'vue'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { contextGenerationKey } from './context'
const mocks = vi.hoisted(() => ({ api: { listAllPackagesPage: vi.fn(), listAllPackages: vi.fn() } }))
vi.mock('./api', () => ({ api: mocks.api }))
vi.mock('./portalkit/ResourceTable.vue', () => ({ default: defineComponent({ props: ['rows', 'query', 'filterValues', 'page'], emits: ['change'], setup: (props, { emit }) => () => h('table-stub', { ...props, onChange: (value: unknown) => emit('change', value) }) }) }))
vi.mock('./portalkit/StatusBadge.vue', () => ({ default: { setup: () => () => null } }))
import PackagesView from './views/PackagesView.vue'
import packagesSource from './views/PackagesView.vue?raw'
interface Deferred<T> {
  promise: Promise<T>
  resolve(value: T): void
  reject(reason?: unknown): void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

interface TreeNode {
  type: string
  props: Record<string, unknown>
  children: TreeNode[]
  options?: TreeNode[]
  parent: TreeNode | null
  text?: string
  setAttribute(name: string, value: string): void
  removeAttribute(name: string): void
  addEventListener?(name: string, handler: unknown): void
  removeEventListener?(name: string, handler: unknown): void
  getRootNode?(): TreeNode
  focus?(): void
}

function textNode(text: string): TreeNode {
  return { type: '#text', props: {}, children: [], parent: null, text, setAttribute() {}, removeAttribute() {} }
}

function createTreeElement(type: string): TreeNode {
  const node: TreeNode = {
    type,
    props: {},
    children: [],
    options: [],
    parent: null,
    setAttribute(name, value) {
      this.props[name] = value
    },
    removeAttribute(name) {
      delete this.props[name]
    },
    addEventListener(name, handler) {
      this.props[`listener:${name}`] = handler
    },
    removeEventListener(name) {
      delete this.props[`listener:${name}`]
    },
    getRootNode() {
      return this
    },
    focus() {
      this.props.focused = true
    },
  }
  return node
}

const rendererOptions: RendererOptions<TreeNode, TreeNode> = {
  patchProp(node, key, _previous, next) {
    node.props[key] = next
  },
  insert(node, parent, anchor) {
    node.parent = parent
    if (parent.type === 'select' && node.type === 'option') parent.options?.push(node)
    if (anchor) {
      const index = parent.children.indexOf(anchor)
      parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
    } else {
      parent.children.push(node)
    }
  },
  remove(node) {
    if (!node.parent) return
    if (node.parent.type === 'select' && node.type === 'option') {
      const optionIndex = node.parent.options?.indexOf(node) ?? -1
      if (optionIndex >= 0) node.parent.options?.splice(optionIndex, 1)
    }
    const index = node.parent.children.indexOf(node)
    if (index >= 0) node.parent.children.splice(index, 1)
    node.parent = null
  },
  createElement: type => createTreeElement(type),
  createText: text => textNode(text),
  createComment: text => ({ type: '#comment', props: {}, children: [], parent: null, text, setAttribute() {}, removeAttribute() {} }),
  setText(node, text) {
    node.text = text
  },
  setElementText(node, text) {
    node.children = [textNode(text)]
    node.children[0].parent = node
  },
  parentNode: node => node.parent,
  nextSibling: node => {
    if (!node.parent) return null
    const index = node.parent.children.indexOf(node)
    return index >= 0 ? node.parent.children[index + 1] || null : null
  },
}

const renderer = createRenderer(rendererOptions)

function compileClientRender(source: string, filename: string): (ctx: unknown, cache: unknown) => unknown {
  const parsed = parse(source, { filename })
  if (!parsed.descriptor.template) throw new Error(`${filename} has no template`)
  const script = compileScript(parsed.descriptor, { id: `behavior-${filename}` })
  const compiled = compileTemplate({
    source: parsed.descriptor.template.content,
    filename,
    id: `behavior-${filename}`,
    // The in-memory renderer models nodes, not browser HTML parsing.
    compilerOptions: { bindingMetadata: script.bindings, hoistStatic: false },
  })
  if (compiled.errors.length) throw compiled.errors[0]
  const code = compiled.code
    .replace(/^import \{([^\n]+)\} from "vue"\n/, (_match, imports: string) => {
      const bindings = imports.split(',').map(binding => {
        const [name, alias] = binding.trim().split(/\s+as\s+/)
        return alias ? `${name}: ${alias}` : name
      }).join(', ')
      return `const { ${bindings} } = VueRuntime\n`
    })
    .replace('export function render', 'function render')
  return new Function('VueRuntime', `${code}\nreturn render`)(VueRuntime) as (ctx: unknown, cache: unknown) => unknown
}

;(PackagesView as unknown as { render: unknown }).render = compileClientRender(packagesSource, 'PackagesView.vue')

function allNodes(root: TreeNode): TreeNode[] {
  return [root, ...root.children.flatMap(allNodes)]
}

function findNode(root: TreeNode, predicate: (node: TreeNode) => boolean): TreeNode | undefined {
  return allNodes(root).find(predicate)
}

async function settle(): Promise<void> {
  for (let index = 0; index < 5; index += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function mount(component: Component, props: Record<string, unknown>) {
  const root = createTreeElement('root')
  const app = renderer.createApp(component, props)
  app.provide(ssrContextKey, { modules: new Set<string>() })
  const contextGeneration = ref(0)
  app.provide(contextGenerationKey, contextGeneration)
  app.mount(root)
  return { app, root, contextGeneration }
}


afterEach(() => { vi.useRealTimers(); vi.clearAllMocks() })
it('queues a single foreground refresh behind polling and retains the current page', async () => {
  vi.useFakeTimers()
  const poll = deferred<{ items: unknown[]; continue: string }>()
  const foreground = deferred<{ items: unknown[]; continue: string }>()
  const row = { name: 'first', repositoryRef: 'repo', type: 'npm', visibility: 'public', ready: true }
  mocks.api.listAllPackagesPage.mockResolvedValueOnce({ items: [row], continue: '' }).mockReturnValueOnce(poll.promise).mockReturnValueOnce(foreground.promise)
  const { app, root } = mount(PackagesView, {})
  await settle()
  const button = () => findNode(root, n => n.type === 'button')!
  await vi.advanceTimersByTimeAsync(30000); await settle()
  expect(mocks.api.listAllPackagesPage).toHaveBeenCalledTimes(2)
  ;(button().props.onClick as () => void)(); (button().props.onClick as () => void)()
  await settle()
  expect(button().props['aria-disabled']).toBe(true)
  expect(mocks.api.listAllPackagesPage).toHaveBeenCalledTimes(2)
  poll.resolve({ items: [row], continue: '' }); await settle()
  expect(mocks.api.listAllPackagesPage).toHaveBeenCalledTimes(3)
  expect(button().props['aria-disabled']).toBe(true)
  foreground.resolve({ items: [row], continue: '' }); await settle()
  expect(button().props['aria-disabled']).toBe(false)
  app.unmount()
})

it('refreshes a filtered complete list without clearing its controls or focus target', async () => {
  const row = { name: 'first', repositoryRef: 'repo', type: 'npm', visibility: 'public', ready: true }
  const next = deferred<unknown[]>()
  mocks.api.listAllPackagesPage.mockReset().mockResolvedValue({ items: [row], continue: '' })
  mocks.api.listAllPackages.mockReset().mockReturnValue(next.promise)
  const { app, root } = mount(PackagesView, {})
  await settle()
  const table = () => findNode(root, n => n.type === 'table-stub')!
  const button = findNode(root, n => n.type === 'button')!
  ;(table().props.onChange as (value: unknown) => void)({ reason: 'query', query: 'first', filters: { type: 'npm', visibility: '', status: '' }, page: 1, pageSize: 25, cursor: null })
  await settle()
  button.focus?.()
  ;(button.props.onClick as () => void)()
  await settle()
  expect(mocks.api.listAllPackages).toHaveBeenCalledTimes(1)
  expect(table().props.query).toBe('first')
  expect((table().props.filterValues as { type: string }).type).toBe('npm')
  expect((table().props.rows as { name: string }[])[0].name).toBe('first')
  expect(findNode(root, n => n.type === 'button')).toBe(button)
  expect(button.props.focused).toBe(true)
  next.resolve([{ ...row, name: 'updated' }]); await settle()
  expect((table().props.rows as { name: string }[])[0].name).toBe('updated')
  expect(table().props.query).toBe('first')
  app.unmount()
})

it('does not promote a pending filtered read after unmount', async () => {
  const read = deferred<unknown[]>()
  mocks.api.listAllPackagesPage.mockReset().mockResolvedValue({ items: [], continue: 'next' })
  mocks.api.listAllPackages.mockReset().mockReturnValue(read.promise)
  const { app, root } = mount(PackagesView, {})
  await settle()
  const state = (root as unknown as { _vnode: { component: { setupState: Record<string, any> } } })._vnode.component.setupState
  const table = findNode(root, n => n.type === 'table-stub')!
  ;(table.props.onChange as (value: unknown) => void)({ reason: 'query', query: 'first', filters: { type: '', visibility: '', status: '' }, page: 1, pageSize: 25, cursor: null })
  await settle()
  expect(mocks.api.listAllPackages).toHaveBeenCalledTimes(1)
  app.unmount()
  read.resolve([{ name: 'first', repositoryRef: 'repo', type: 'npm' }]); await settle()
  expect(state.packageMode).toBe('server')
  expect(state.packages).toEqual([])
})

it('promotes a completed full read for a filter changed while the read is pending', async () => {
  const read = deferred<unknown[]>()
  mocks.api.listAllPackagesPage.mockReset().mockResolvedValue({ items: [], continue: 'next' })
  mocks.api.listAllPackages.mockReset().mockReturnValue(read.promise)
  const { app, root } = mount(PackagesView, {})
  await settle()
  const table = () => findNode(root, n => n.type === 'table-stub')!
  const change = (query: string) => (table().props.onChange as (value: unknown) => void)({ reason: 'query', query, filters: { type: '', visibility: '', status: '' }, page: 1, pageSize: 25, cursor: null })
  change('first'); await settle()
  change('second'); await settle()
  const row = { name: 'second', repositoryRef: 'repo', type: 'npm' }
  read.resolve([row]); await settle()
  expect(table().props.query).toBe('second')
  expect((table().props.rows as { name: string }[])[0].name).toBe('second')
  expect(mocks.api.listAllPackages).toHaveBeenCalledTimes(1)
  app.unmount()
})
