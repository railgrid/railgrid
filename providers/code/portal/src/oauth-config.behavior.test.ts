import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h, KeepAlive, nextTick, provide, ref, ssrContextKey, type Component, type RendererOptions } from '@vue/runtime-core'
import * as VueRuntime from 'vue'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import type { VNode } from 'vue'

import { createResourceDeletions } from './refresh'
import { contextGenerationKey } from './context'

const mocks = vi.hoisted(() => ({
  api: {
    listConnections: vi.fn(),
    listRepositories: vi.fn(),
    listRepositoriesPage: vi.fn(),
    oauthConfig: vi.fn(),
    connect: vi.fn(),
    getConnection: vi.fn(),
    createRepository: vi.fn(),
    getRepository: vi.fn(),
    deleteRepository: vi.fn(),
  },
  confirmDialog: vi.fn(),
  toast: vi.fn(),
}))

vi.mock('./api', async (importOriginal) => ({
  api: mocks.api,
  normalizeResourceName: (await importOriginal<typeof import('./api')>()).normalizeResourceName,
  isPreDispatchError: (await importOriginal<typeof import('./api')>()).isPreDispatchError,
  isRejectedConnectionWrite: (await importOriginal<typeof import('./api')>()).isRejectedConnectionWrite,
}))
vi.mock('./portalkit/confirm', () => ({ confirmDialog: mocks.confirmDialog }))
vi.mock('./portalkit/toast', () => ({ toast: mocks.toast }))
vi.mock('./portalkit/ResourceTable.vue', async () => {
  const { h } = await import('vue')
  type StubSlots = Record<string, () => VNode[]>
  return {
    default: {
      setup: (_props: unknown, { slots }: { slots: StubSlots }) => () => h('div', { class: 'resource-table-stub' }, [
        slots.default?.(),
      ]),
    },
  }
})
vi.mock('./portalkit/ResourceTableDeleteButton.vue', async () => {
  const { h } = await import('vue')
  return { default: { setup: () => () => h('button', 'Delete') } }
})
vi.mock('./portalkit/ActionMenu.vue', async () => {
  const { h } = await import('vue')
  return { default: { setup: () => () => h('div', { class: 'action-menu-stub' }) } }
})
vi.mock('./portalkit/StatusBadge.vue', async () => {
  const { h } = await import('vue')
  return { default: { setup: () => () => h('span', 'status') } }
})
vi.mock('./portalkit/FirstRunGuide.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      props: { title: String, primaryLabel: String, secondaryLabel: String },
      emits: ['primary', 'secondary'],
      setup: (props, { emit, slots }) => () => h('section', { class: 'k-first-run' }, [
        h('h3', props.title),
        ...(slots.actions ? slots.actions() : [
          h('button', { onClick: () => emit('primary') }, props.primaryLabel),
          props.secondaryLabel && h('button', { onClick: () => emit('secondary') }, props.secondaryLabel),
        ]),
      ]),
    }),
  }
})
vi.mock('lucide-vue-next', async () => {
  const { h } = await import('vue')
  const icon = { setup: () => () => h('span', { 'aria-hidden': 'true' }) }
  return { ChevronDown: icon, ArrowLeft: icon, ArrowRight: icon, Check: icon, CircleDot: icon, Info: icon, Link2: icon }
})

import ConnectionsView from './views/ConnectionsView.vue'
import RepositoriesView from './views/RepositoriesView.vue'
import ConnectionCreateView from './views/ConnectionCreateView.vue'
import CreateGuidance from './portalkit/CreateGuidance.vue'
import createGuidanceSource from './portalkit/CreateGuidance.vue?raw'
import FormSelect from './portalkit/FormSelect.vue'
import formSelectSource from './portalkit/FormSelect.vue?raw'
import RepositoryCreateView from './views/RepositoryCreateView.vue'
import connectionsSource from './views/ConnectionsView.vue?raw'
import repositoriesSource from './views/RepositoriesView.vue?raw'
import connectionCreateSource from './views/ConnectionCreateView.vue?raw'
import repositoryCreateSource from './views/RepositoryCreateView.vue?raw'

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

;(FormSelect as unknown as { render: unknown }).render = compileClientRender(formSelectSource, 'FormSelect.vue')
;(CreateGuidance as unknown as { render: unknown }).render = compileClientRender(createGuidanceSource, 'CreateGuidance.vue')
;(ConnectionsView as unknown as { render: unknown }).render = compileClientRender(connectionsSource, 'ConnectionsView.vue')
;(RepositoriesView as unknown as { render: unknown }).render = compileClientRender(repositoriesSource, 'RepositoriesView.vue')
;(ConnectionCreateView as unknown as { render: unknown }).render = compileClientRender(connectionCreateSource, 'ConnectionCreateView.vue')
;(RepositoryCreateView as unknown as { render: unknown }).render = compileClientRender(repositoryCreateSource, 'RepositoryCreateView.vue')

function allNodes(root: TreeNode): TreeNode[] {
  return [root, ...root.children.flatMap(allNodes)]
}

function textContent(node: TreeNode): string {
  return node.text ?? node.children.map(textContent).join('')
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
  const app = renderer.createApp(component, component === ConnectionCreateView ? { recoveryScope: 'test', ...props } : props)
  app.provide(ssrContextKey, { modules: new Set<string>() })
  const contextGeneration = ref(0)
  app.provide(contextGenerationKey, contextGeneration)
  app.mount(root)
  return { app, root, contextGeneration }
}

let previousWindow: unknown
let previousDocument: unknown
let previousShadowRoot: unknown

beforeEach(() => {
  previousWindow = (globalThis as typeof globalThis & { window?: unknown }).window
  previousDocument = (globalThis as typeof globalThis & { Document?: unknown }).Document
  previousShadowRoot = (globalThis as typeof globalThis & { ShadowRoot?: unknown }).ShadowRoot
  Object.defineProperty(globalThis, 'Document', { configurable: true, value: class Document {} })
  Object.defineProperty(globalThis, 'ShadowRoot', { configurable: true, value: class ShadowRoot {} })
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      sessionStorage: (() => { const data = new Map<string, string>(); return { getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => data.set(key, value), removeItem: (key: string) => data.delete(key) } })(),
      setInterval: () => 0,
      clearInterval: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
    },
  })
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.listRepositories.mockResolvedValue([])
  mocks.api.listRepositoriesPage.mockResolvedValue({ items: [] })
  mocks.api.oauthConfig.mockResolvedValue({ enabled: false })
  mocks.api.connect.mockResolvedValue({ name: 'created' })
  mocks.api.getConnection.mockReset()
  mocks.api.createRepository.mockResolvedValue({ name: 'created-repository' })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  if (previousDocument === undefined) {
    Reflect.deleteProperty(globalThis, 'Document')
  } else {
    Object.defineProperty(globalThis, 'Document', { configurable: true, value: previousDocument })
  }
  if (previousShadowRoot === undefined) {
    Reflect.deleteProperty(globalThis, 'ShadowRoot')
  } else {
    Object.defineProperty(globalThis, 'ShadowRoot', { configurable: true, value: previousShadowRoot })
  }
  if (previousWindow === undefined) {
    Reflect.deleteProperty(globalThis, 'window')
  } else {
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
  }
})

describe('OAuth configuration error states', () => {
  it('offers token setup while configuration is pending and after it times out, then accepts a retry', async () => {
    vi.useFakeTimers()
    try {
      const stalled = deferred<{ enabled: boolean; startURL: string }>()
      mocks.api.oauthConfig.mockReset().mockReturnValueOnce(stalled.promise).mockResolvedValueOnce({ enabled: true, startURL: '/oauth/github/start' })
      const useToken = vi.fn()
      const view = mount(ConnectionCreateView, { method: 'github', deletions: createResourceDeletions(), onUseToken: useToken })
      await settle()
      const fallback = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Use a personal access token')!
      expect(fallback).toBeDefined()
      ;(fallback.props.onClick as () => void)()
      expect(useToken).toHaveBeenCalledOnce()
      await vi.advanceTimersByTimeAsync(15000)
      await settle()
      expect(textContent(view.root)).toContain('The request timed out.')
      expect(findNode(view.root, node => node.type === 'button' && textContent(node) === 'Use a personal access token')).toBeDefined()
      const retry = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Retry GitHub sign-in')!
      ;(retry.props.onClick as () => void)()
      await settle()
      expect(textContent(view.root)).toContain('Continue with GitHub')
      stalled.resolve({ enabled: false, startURL: '' })
      await settle()
      expect(textContent(view.root)).toContain('Continue with GitHub')
      expect(textContent(view.root)).not.toContain('The request timed out.')
      view.app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('offers token setup after a configuration error and ignores a stale workspace response', async () => {
    const stale = deferred<{ enabled: boolean; startURL: string }>()
    mocks.api.oauthConfig.mockReset().mockReturnValueOnce(stale.promise).mockRejectedValueOnce(new Error('offline'))
    const view = mount(ConnectionCreateView, { method: 'github', deletions: createResourceDeletions() })
    await settle()
    view.contextGeneration.value++
    await settle()
    expect(textContent(view.root)).toContain('offline')
    expect(findNode(view.root, node => node.type === 'button' && textContent(node) === 'Use a personal access token')).toBeDefined()
    stale.resolve({ enabled: true, startURL: '/oauth/github/start' })
    await settle()
    expect(textContent(view.root)).toContain('offline')
    expect(textContent(view.root)).not.toContain('Continue with GitHub')
    view.app.unmount()
  })
  it('uses fresh repository policy and identity for collection deletion', async () => {
    const row = { name: 'orders', uid: 'uid-1', resourceVersion: '1', repo: 'orders', existingOnly: false }
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listRepositoriesPage.mockResolvedValue({ items: [row] })
    mocks.api.getRepository.mockResolvedValue({ ...row, resourceVersion: '2', existingOnly: true })
    mocks.api.deleteRepository.mockResolvedValue(undefined)
    mocks.confirmDialog.mockResolvedValue(true)
    const child = ref<any>()
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(RepositoriesView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test' }) }) })
    const { app } = mount(host, {})
    await settle()
    await child.value.$.setupState.remove(row)
    expect(mocks.confirmDialog).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('current configuration protects') }))
    expect(mocks.api.deleteRepository).toHaveBeenCalledWith('orders', { uid: 'uid-1', resourceVersion: '2' })
    app.unmount()
  })
  it('does not delete after authority changes during the collection confirmation', async () => {
    const row = { name: 'orders', uid: 'uid-1', resourceVersion: '1', repo: 'orders' }
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listRepositoriesPage.mockResolvedValue({ items: [row] })
    mocks.api.getRepository.mockResolvedValue(row)
    const confirmation = deferred<boolean>()
    mocks.confirmDialog.mockReturnValueOnce(confirmation.promise)
    const child = ref<any>()
    const generation = ref(0)
    const host = defineComponent({ setup: () => { provide(contextGenerationKey, generation); return () => h(KeepAlive, null, { default: () => h(RepositoriesView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test' }) }) } })
    const { app } = mount(host, {})
    await settle()
    const pending = child.value.$.setupState.remove(row)
    await settle()
    generation.value++
    confirmation.resolve(true)
    await pending
    expect(mocks.api.deleteRepository).not.toHaveBeenCalled()
    app.unmount()
  })
  it.each([false, true])('offers the supported prerequisite method when OAuth enabled is %s', async (enabled) => {
    mocks.api.oauthConfig.mockResolvedValue({ enabled, startURL: '/oauth/github/start' })
    const selected: string[] = []
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(ConnectionsView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' , prerequisite: true,
      onCreate: (method: string) => selected.push(method),
    }) }) })
    const { app, root } = mount(host, {})
    await settle()
    const label = enabled ? 'Connect with GitHub' : 'Add token manually'
    const button = findNode(root, node => node.type === 'button' && textContent(node) === label)
    expect(button).toBeDefined()
    ;(button!.props.onClick as () => void)()
    expect(selected).toEqual([enabled ? 'github' : 'token'])
    app.unmount()
  })

  it.each(['connections', 'repositories'] as const)('keeps the %s table mounted after the last row disappears and resets on workspace change', async (kind) => {
    const child = ref<any>()
    const workspace = ref('one')
    const row = { name: 'example', repo: 'example', owner: 'owner', validated: true }
    mocks.api.listConnections.mockResolvedValue(kind === 'connections' ? [row] : [])
    mocks.api.listRepositoriesPage.mockResolvedValue({ items: kind === 'repositories' ? [row] : [] })
    const component: Component = kind === 'connections' ? ConnectionsView : RepositoriesView
    const host = defineComponent({ setup: () => () => h(KeepAlive, { key: workspace.value }, { default: () => h(component, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test'  }) }) })
    const { app, root } = mount(host, {})
    await settle()
    const table = findNode(root, node => node.props.class === 'resource-table-stub')
    expect(table).toBeDefined()
    const state = child.value.$.setupState
    if (kind === 'repositories') state.repositoryQuery = 'example'
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listRepositoriesPage.mockResolvedValue({ items: [] })
    mocks.api.listRepositories.mockResolvedValue([])
    if (kind === 'connections') await state.load('background')
    else await state.loadRepositories('background')
    await settle()
    expect(findNode(root, node => node.props.class === 'resource-table-stub')).toBe(table)
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeUndefined()
    if (kind === 'repositories') {
      state.repositoryQuery = ''
      await settle()
      expect(findNode(root, node => node.props.class === 'resource-table-stub')).toBe(table)
    }
    workspace.value = 'two'
    await settle()
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    app.unmount()
  })

  it('keeps repository prerequisite guidance through failed refreshes and retries', async () => {
    const child = ref<any>()
    const createConnection = vi.fn()
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(RepositoriesView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test' , onCreateConnection: createConnection }) }) })
    const { app, root } = mount(host, {})
    await settle()
    const state = child.value.$.setupState
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    mocks.api.listRepositoriesPage.mockRejectedValueOnce(new Error('Repository refresh failed'))
    await state.loadRepositories('background')
    await settle()
    expect(textContent(root)).toContain('Repository refresh failed')
    const guide = findNode(root, node => node.props.class === 'k-first-run')!
    ;(findNode(guide, node => node.type === 'button')!.props.onClick as () => void)()
    expect(createConnection).toHaveBeenCalledOnce()
    const retryRead = deferred<{ items: never[] }>()
    mocks.api.listRepositoriesPage.mockReturnValueOnce(retryRead.promise)
    ;(findNode(root, node => node.type === 'button' && textContent(node) === 'Retry')!.props.onClick as () => void)()
    await settle()
    const busyRetry = findNode(root, node => node.type === 'button' && textContent(node) === 'Refreshing…')!
    expect(busyRetry.props['aria-disabled']).toBe(true)
    expect(busyRetry.props.disabled).toBeUndefined()
    ;(busyRetry.props.onClick as () => void)()
    expect(mocks.api.listRepositoriesPage).toHaveBeenCalledTimes(3)
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    retryRead.resolve({ items: [] })
    await settle()
    expect(textContent(root)).not.toContain('Repository refresh failed')
    mocks.api.listConnections.mockRejectedValueOnce(new Error('Connection refresh failed'))
    await state.loadConnections('background')
    await settle()
    expect(textContent(root)).toContain('Showing cached connection choices. Connection refresh failed')
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    ;(findNode(root, node => node.type === 'button' && textContent(node) === 'Retry connections')!.props.onClick as () => void)()
    await settle()
    expect(textContent(root)).not.toContain('Connection refresh failed')
    app.unmount()
  })

  it('does not show repository guidance before both initial reads succeed', async () => {
    mocks.api.listConnections.mockRejectedValueOnce(new Error('Initial connection read failed'))
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(RepositoriesView, { deletions: createResourceDeletions(), recoveryScope: 'test'  }) }) })
    const { app, root } = mount(host, {})
    await settle()
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeUndefined()
    app.unmount()
  })

  it('retains an empty connection guide through a failed background refresh and retry', async () => {
    const child = ref<any>()
    const create = vi.fn()
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(ConnectionsView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test' , onCreate: create }) }) })
    const { app, root } = mount(host, {})
    await settle()
    const state = child.value.$.setupState
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    mocks.api.listConnections.mockRejectedValueOnce(new Error('Refresh failed'))
    await state.load('background')
    await settle()
    expect(textContent(root)).toContain('Showing the last successful result. Refresh failed')
    const guide = findNode(root, node => node.props.class === 'k-first-run')!
    const action = findNode(guide, node => node.type === 'button')!
    ;(action.props.onClick as () => void)()
    expect(create).toHaveBeenCalledWith('token')
    const retryRead = deferred<never[]>()
    mocks.api.listConnections.mockReturnValueOnce(retryRead.promise)
    const retry = findNode(root, node => node.type === 'button' && textContent(node) === 'Retry')!
    ;(retry.props.onClick as () => void)()
    await settle()
    const busyRetry = findNode(root, node => node.type === 'button' && textContent(node) === 'Refreshing…')!
    expect(busyRetry.props['aria-disabled']).toBe(true)
    expect(busyRetry.props.disabled).toBeUndefined()
    ;(busyRetry.props.onClick as () => void)()
    expect(mocks.api.listConnections).toHaveBeenCalledTimes(3)
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    vi.stubGlobal('document', { activeElement: state.readRetry, body: {} })
    retryRead.resolve([])
    await settle()
    expect(findNode(root, node => node.type === 'h2' && textContent(node) === 'Connections')?.props.focused).toBe(true)
    expect(textContent(root)).not.toContain('Refresh failed')
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeDefined()
    app.unmount()
  })

  it.each([true, false])('restores focus for populated results only when retry still owns focus: %s', async (ownsFocus) => {
    const child = ref<any>()
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(ConnectionsView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test'  }) }) })
    const { app, root } = mount(host, {})
    await settle()
    mocks.api.listConnections.mockRejectedValueOnce(new Error('Refresh failed'))
    child.value.$.setupState.load('background')
    await settle()
    const pending = deferred<any[]>()
    mocks.api.listConnections.mockReturnValueOnce(pending.promise)
    const retry = findNode(root, node => node.type === 'button' && textContent(node) === 'Retry')!
    ;(retry.props.onClick as () => void)()
    await settle()
    vi.stubGlobal('document', { activeElement: ownsFocus ? child.value.$.setupState.readRetry : {}, body: {} })
    pending.resolve([{ name: 'new-connection' }])
    await settle()
    expect(!!findNode(root, node => node.type === 'h2' && textContent(node) === 'Connections')?.props.focused).toBe(ownsFocus)
    app.unmount()
  })

  it('does not show a connection guide when the initial read fails', async () => {
    mocks.api.listConnections.mockRejectedValueOnce(new Error('Initial read failed'))
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => h(ConnectionsView, { deletions: createResourceDeletions(), recoveryScope: 'test'  }) }) })
    const { app, root } = mount(host, {})
    await settle()
    expect(findNode(root, node => node.props.class === 'k-first-run')).toBeUndefined()
    app.unmount()
  })

  it('shows a retry action on the connections collection when configuration fails', async () => {
    const configRequest = deferred<{ enabled: boolean }>()
    mocks.api.oauthConfig.mockReturnValue(configRequest.promise)
    const deletions = createResourceDeletions()
    const host = defineComponent({
      setup: () => () => h(KeepAlive, null, { default: () => h(ConnectionsView, { deletions }) }),
    })
    const { app, root } = mount(host, {})
    await settle()

    configRequest.reject(new Error('oauth backend unavailable'))
    await settle()

    expect(textContent(root)).toContain('GitHub sign-in configuration could not be loaded: oauth backend unavailable')
    expect(textContent(root)).toContain('Retry GitHub sign-in')
    expect(textContent(root)).toContain('Connect a GitHub account')
    expect(textContent(root)).toContain('Add token manually')
    expect(findNode(root, node => String(node.props.class || '').split(' ').includes('k-first-run'))).toBeDefined()
    expect(findNode(root, node => node.props.role === 'alert')).toBeDefined()
    app.unmount()
  })

  it('shows GitHub guidance only after a successful retry on the creation route', async () => {
    const configRequest = deferred<{ enabled: boolean }>()
    mocks.api.oauthConfig.mockReturnValueOnce(configRequest.promise)
      .mockResolvedValueOnce({ enabled: true, startURL: '/oauth/github/start' })
    const { app, root } = mount(ConnectionCreateView, {
      method: 'github',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
    })
    await settle()
    expect(textContent(root)).toContain('Checking GitHub sign-in…')
    expect(findNode(root, node => node.type === 'aside')).toBeUndefined()

    configRequest.reject(new Error('oauth backend unavailable'))
    await settle()

    expect(textContent(root)).toContain('GitHub sign-in configuration could not be loaded: oauth backend unavailable')
    expect(findNode(root, node => node.props.role === 'alert')).toBeDefined()
    expect(findNode(root, node => node.type === 'aside')).toBeUndefined()
    expect(textContent(root)).not.toContain('Go back to add a token manually.')
    expect(textContent(root)).not.toContain('Continue with GitHub')
    const retry = findNode(root, node => node.type === 'button' && textContent(node) === 'Retry GitHub sign-in')
    expect(retry?.props.onClick).toBeTypeOf('function')
    await (retry?.props.onClick as () => Promise<void>)()
    await settle()

    expect(textContent(root)).toContain('GitHub opens in a new window.')
    expect(textContent(root)).toContain('Continue with GitHub')
    const guidance = findNode(root, node => node.type === 'aside')
    expect(guidance).toBeDefined()
    expect(textContent(guidance!)).toContain('After authorization')
    expect(findNode(root, node => node.props.role === 'alert')).toBeUndefined()
    app.unmount()
  })

  it('directs users to manual token setup when GitHub sign-in is disabled', async () => {
    const useToken = vi.fn()
    const { app, root } = mount(ConnectionCreateView, {
      method: 'github',
      deletions: createResourceDeletions(), onUseToken: useToken,
    })
    await settle()

    expect(textContent(root)).toContain('GitHub sign-in is unavailable.')
    const fallback = findNode(root, node => node.type === 'button' && textContent(node) === 'Use a personal access token')
    expect(fallback).toBeDefined()
    ;(fallback!.props.onClick as () => void)()
    expect(useToken).toHaveBeenCalledOnce()
    expect(textContent(root)).not.toContain('Continue with GitHub')
    expect(textContent(root)).not.toContain('Retry GitHub sign-in')
    expect(findNode(root, node => node.type === 'aside')).toBeUndefined()
    app.unmount()
  })
})

describe('connection creation authority fencing', () => {
  const existingConnection = {
    name: 'github-prod',
    uid: 'github-prod-uid',
    provider: 'github',
    type: 'pat',
    owner: 'octocat',
    secretName: 'github-prod-token',
    scopes: [],
    validated: true,
  }

  const activeRepository = {
    name: 'github-prod',
    uid: 'github-prod-repository-uid',
    connectionRef: 'github-prod',
    repo: 'github-prod',
    visibility: 'private',
    ready: true,
  }

  const activeConnection = {
    name: 'github-prod',
    uid: 'github-prod-connection-uid',
    provider: 'github',
    type: 'pat',
    owner: 'octocat',
    secretName: 'github-prod-token',
    scopes: [],
    validated: true,
  }

  function setInput(root: TreeNode, placeholder: string, value: string): void {
    const input = findNode(root, node => node.type === 'input' && node.props.placeholder === placeholder)
    if (!input) throw new Error(`input ${placeholder} was not rendered`)
    const update = input.props['onUpdate:modelValue'] ?? input.props.onInput
    if (typeof update !== 'function') throw new Error(`input ${placeholder} had no v-model handler`)
    update(typeof input.props['onUpdate:modelValue'] === 'function' ? value : { target: { value } })
  }

  async function submit(root: TreeNode): Promise<void> {
    const form = findNode(root, node => node.type === 'form')
    if (!form) throw new Error('connection form was not rendered')
    const handler = form.props.onSubmit
    if (typeof handler !== 'function') throw new Error('connection form had no submit handler')
    await handler({ preventDefault() {} })
  }

  it('emits one informational toast after a current connection create succeeds', async () => {
    const created = vi.fn()
    const { app, root } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      onCreated: created,
    })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'new-secret')

    await submit(root)
    await settle()

    expect(mocks.toast).toHaveBeenCalledTimes(1)
    expect(mocks.toast).toHaveBeenCalledWith('info', 'Connection creation requested for created.')
    expect(created).toHaveBeenCalledWith('created')
    app.unmount()
  })

  it('reports required connection fields and focuses the first invalid input', async () => {
    mocks.api.listConnections.mockResolvedValue([])
    const { app, root } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
    })
    await settle()

    await submit(root)
    await settle()

    expect(textContent(root)).toContain('Enter a connection name.')
    expect(textContent(root)).toContain('Enter a GitHub account or organization.')
    expect(textContent(root)).toContain('Enter a personal access token.')
    const first = findNode(root, node => node.type === 'input' && node.props.id === 'code-connection-name')
    expect(first?.props['aria-invalid']).toBe('true')
    expect(first?.props.focused).toBe(true)
    app.unmount()
  })

  it('submits an organization import and hides creation-only settings', async () => {
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test' , mode: 'import' })
    await settle()
    setInput(root, 'my-service', 'providers')
    setInput(root, 'owner/repository', 'railgrid/providers')
    expect(textContent(root)).not.toContain('Visibility')
    expect(textContent(root)).not.toContain('Add a README')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).toHaveBeenCalledWith(expect.objectContaining({ mode: 'import', repo: 'railgrid/providers', name: 'providers' }))
    app.unmount()
  })

  it.each(['create', 'import'] as const)('keeps the repository format hint associated after %s validation fails', async (mode) => {
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test', mode })
    await settle()
    setInput(root, 'my-service', 'providers')
    const remote = findNode(root, node => node.props.id === 'code-repository-remote')!
    ;(remote.props['onUpdate:modelValue'] as (value: string) => void)(mode === 'import' ? 'invalid' : 'owner/repo')
    await submit(root)
    const input = findNode(root, node => node.props.id === 'code-repository-remote')
    expect(input?.props['aria-describedby']).toBe('code-repository-remote-error code-repository-remote-hint')
    expect(findNode(root, node => node.props.id === 'code-repository-remote-error')).toBeDefined()
    expect(findNode(root, node => node.props.id === 'code-repository-remote-hint')).toBeDefined()
    app.unmount()
  })

  it.each([true, false])('hands focus to the connection selector after recheck only if focus remains (remains: %s)', async (remains) => {
    const read = deferred<typeof activeConnection[]>()
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(read.promise)
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test'  })
    await settle()
    const button = findNode(root, node => node.type === 'button' && textContent(node) === 'Check again')!
    const doc = { activeElement: button as unknown, body: {}, getElementById: () => ({}) }
    vi.stubGlobal('document', doc)
    ;(button.props.onClick as () => void)()
    if (!remains) doc.activeElement = {}
    read.resolve([activeConnection])
    await settle()
    const selector = findNode(root, node => node.props.id === 'code-repository-connection')
    expect(!!selector?.props.focused).toBe(remains)
    app.unmount()
  })

  it('shows first-run checking status and prevents repeated requests', async () => {
    const read = deferred<typeof activeConnection[]>()
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(read.promise)
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test'  })
    await settle()
    const button = findNode(root, node => node.type === 'button' && textContent(node) === 'Check again')!
    ;(button.props.onClick as () => void)()
    await settle()
    expect(button.props['aria-disabled']).toBe(true)
    expect(textContent(root)).toContain('Checking connections…')
    ;(button.props.onClick as () => void)()
    expect(mocks.api.listConnections).toHaveBeenCalledTimes(2)
    read.resolve([])
    await settle()
    expect(textContent(root)).toContain('Check again')
    app.unmount()
  })

  it.each(['error', 'draft'] as const)('hands focus off a disappearing %s retry without stealing moved focus', async (kind) => {
    for (const remains of [true, false]) {
      const read = deferred<typeof activeConnection[]>()
      mocks.api.listConnections.mockReset()
      if (kind === 'error') mocks.api.listConnections.mockRejectedValueOnce(new Error('Connection failed'))
      else mocks.api.listConnections.mockResolvedValueOnce([])
      mocks.api.listConnections.mockReturnValueOnce(read.promise)
      const draft = { name: 'draft', repo: '', connectionRef: activeConnection.name, visibility: 'private' as const, description: '', autoInit: true }
      const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test' , initialDraft: kind === 'draft' ? draft : undefined })
      await settle()
      const retry = findNode(root, node => node.type === 'button' && textContent(node) === (kind === 'error' ? 'Retry connections' : 'Check again'))!
      const doc = { activeElement: retry as unknown, body: {}, getElementById: () => ({}) }
      vi.stubGlobal('document', doc)
      ;(retry.props.onClick as () => void)()
      if (!remains) doc.activeElement = {}
      read.resolve([activeConnection])
      await settle()
      const selector = findNode(root, node => node.props.id === 'code-repository-connection')
      expect(!!selector?.props.focused).toBe(remains)
      app.unmount()
    }
  })

  it('waits for an older repository walk before starting a fresh submission preflight', async () => {
    const oldWalk = deferred<typeof activeRepository[]>()
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.listRepositories.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(oldWalk.promise).mockResolvedValueOnce([activeRepository])
    const child = ref<any>()
    const host = defineComponent({ setup: () => () => h(RepositoryCreateView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test'  }) })
    const { app, root } = mount(host, {})
    await settle()
    setInput(root, 'my-service', activeRepository.name)
    void child.value.$.setupState.loadRepositories(true)
    await settle()
    const pending = submit(root)
    await settle()
    expect(mocks.api.listRepositories).toHaveBeenCalledTimes(2)
    oldWalk.resolve([])
    await pending
    await settle()
    expect(mocks.api.listRepositories).toHaveBeenCalledTimes(3)
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('already used in this workspace')
    app.unmount()
  })

  it('does not start a fresh preflight after the workspace changes during an older walk', async () => {
    const oldWalk = deferred<typeof activeRepository[]>()
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.listRepositories.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(oldWalk.promise)
    const child = ref<any>()
    const host = defineComponent({ setup: () => () => h(RepositoryCreateView, { ref: child, deletions: createResourceDeletions(), recoveryScope: 'test'  }) })
    const { app, root, contextGeneration } = mount(host, {})
    await settle()
    setInput(root, 'my-service', 'my-repository')
    void child.value.$.setupState.loadRepositories(true)
    await settle()
    const pending = submit(root)
    await settle()
    contextGeneration.value += 1
    oldWalk.resolve([])
    await pending
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    app.unmount()
  })

  it('locks the submitted repository draft until the request settles', async () => {
    const mutation = deferred<typeof activeRepository>()
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.createRepository.mockReturnValueOnce(mutation.promise)
    const created = vi.fn()
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test' , onCreated: created })
    await settle()
    setInput(root, 'my-service', 'submitted-name')
    const pending = submit(root)
    await settle()

    const form = findNode(root, node => node.type === 'form')!
    const controls = allNodes(form).filter(node => node.type === 'input' || node.props.role === 'combobox')
    expect(controls.length).toBeGreaterThanOrEqual(6)
    expect(controls.every(control => control.props.disabled === true)).toBe(true)
    expect(form.props['aria-busy']).toBe(true)
    expect(textContent(root)).toContain('Requesting repository creation')
    expect(mocks.api.createRepository).toHaveBeenCalledWith(expect.objectContaining({ name: 'submitted-name' }))

    mutation.reject(new Error('Request failed'))
    await pending
    await settle()
    expect(created).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('may have completed')
    expect(allNodes(form).filter(node => node.type === 'input' || node.props.role === 'combobox').every(control => !control.props.disabled)).toBe(true)
    expect((findNode(root, node => node.props.id === 'code-repository-name') as TreeNode & { value?: string }).value).toBe('submitted-name')
    app.unmount()
  })

  it('reports required repository fields and focuses the first invalid control', async () => {
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.listRepositories.mockResolvedValue([])
    const { app, root } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
    })
    await settle()

    await submit(root)
    await settle()

    expect(textContent(root)).toContain('Enter a name for this repository in Railgrid.')
    const first = findNode(root, node => node.type === 'input' && node.props.id === 'code-repository-name')
    expect(first?.props['aria-invalid']).toBe('true')
    expect(first?.props.focused).toBe(true)
    app.unmount()
  })

  it('focuses repository setup on the missing connection and routes its action', async () => {
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listRepositories.mockResolvedValue([])
    const createConnection = vi.fn()
    const { app, root } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' , mode: 'import', onCreateConnection: createConnection,
    })
    await settle()
    expect(textContent(root)).toContain('Connect GitHub first')
    expect(findNode(root, node => node.type === 'form')).toBeUndefined()
    const action = findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Add connection')!
    ;(action.props.onClick as () => void)()
    expect(createConnection).toHaveBeenCalledOnce()
    app.unmount()
  })

  it('keeps the draft and error visible if the selected connection disappears during preflight', async () => {
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([activeConnection]).mockResolvedValueOnce([])
    mocks.api.listRepositories.mockResolvedValue([])
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test'  })
    await settle()
    setInput(root, 'my-service', 'my-draft')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('is no longer active')
    expect(textContent(root)).toContain('return to your repository details')
    expect((findNode(root, node => node.props.placeholder === 'my-service') as (TreeNode & { value?: string }) | undefined)?.value).toBe('my-draft')
    expect(findNode(root, node => node.type === 'form')).toBeDefined()
    app.unmount()
  })

  it('detects a repository collision after truncation removes a trailing hyphen', async () => {
    const name = 'a'.repeat(252)
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.listRepositories.mockResolvedValue([{ ...activeRepository, name }])
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test' , mode: 'import' })
    await settle()
    setInput(root, 'my-service', `${name}-b`)
    setInput(root, 'owner/repository', 'owner/repo')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain(`The name "${name}" is already used`)
    app.unmount()
  })

  it('restores repository details while reconciling a removed connection', async () => {
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    const { app, root } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' , mode: 'import',
      initialDraft: { name: 'my-draft', repo: 'owner/repo', connectionRef: 'removed', visibility: 'private', description: 'notes', autoInit: false },
    })
    await settle()
    expect((findNode(root, node => node.props.id === 'code-repository-name') as (TreeNode & { value?: string }) | undefined)?.value).toBe('my-draft')
    expect((findNode(root, node => node.props.id === 'code-repository-remote') as (TreeNode & { value?: string }) | undefined)?.value).toBe('owner/repo')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('Select a GitHub connection.')
    app.unmount()
  })

  it('does not silently switch owners when the selected connection disappears during preflight', async () => {
    const other = { ...activeConnection, name: 'other-connection', owner: 'other-owner' }
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([activeConnection, other]).mockResolvedValueOnce([other])
    mocks.api.listRepositories.mockResolvedValue([])
    const { app, root } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test'  })
    await settle()
    setInput(root, 'my-service', 'my-draft')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('is no longer active')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('Select a GitHub connection.')
    app.unmount()
  })

  it('keeps a returned draft visible and rechecks a connection that has not appeared yet', async () => {
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([]).mockResolvedValueOnce([activeConnection])
    const { app, root } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      initialDraft: { name: 'my-draft', repo: '', connectionRef: activeConnection.name, visibility: 'private', description: '', autoInit: true },
    })
    await settle()
    expect(findNode(root, node => node.type === 'form')).toBeDefined()
    expect((findNode(root, node => node.props.id === 'code-repository-name') as (TreeNode & { value?: string }) | undefined)?.value).toBe('my-draft')
    const retry = findNode(root, node => node.type === 'button' && textContent(node) === 'Check again')!
    await (retry.props.onClick as () => Promise<void>)()
    await settle()
    expect(textContent(root)).not.toContain('No available GitHub connections')
    expect((findNode(root, node => node.props.id === 'code-repository-name') as (TreeNode & { value?: string }) | undefined)?.value).toBe('my-draft')
    expect(textContent(findNode(root, node => node.props.id === 'code-repository-connection')!)).toContain('github-prod')
    app.unmount()
  })

  it('does not replace an explicit connection choice when the returned connection appears', async () => {
    const other = { ...activeConnection, name: 'other', owner: 'other-owner' }
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([other]).mockResolvedValueOnce([other, activeConnection])
    const { app, root, contextGeneration } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      initialDraft: { name: 'draft', repo: '', connectionRef: activeConnection.name, visibility: 'private', description: '', autoInit: true },
    })
    await settle()
    function findSelect(node: VNode): VNode | undefined {
      if (node.type === FormSelect && node.props?.id === 'code-repository-connection') return node
      if (node.component) return findSelect(node.component.subTree)
      if (Array.isArray(node.children)) {
        for (const child of node.children) {
          if (child && typeof child === 'object' && '__v_isVNode' in child) {
            const match = findSelect(child as VNode)
            if (match) return match
          }
        }
      }
    }
    const select = findSelect((root as unknown as { _vnode: VNode })._vnode)!
    select.component!.emit('update:modelValue', 'other')
    await settle()
    contextGeneration.value += 1
    await settle()
    expect(textContent(findNode(root, node => node.props.id === 'code-repository-connection')!)).toContain('other-owner')
    app.unmount()
  })

  it('can recheck a returned connection while another choice is available', async () => {
    const other = { ...activeConnection, name: 'other', owner: 'other-owner' }
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([other]).mockResolvedValueOnce([other, activeConnection])
    const { app, root } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      initialDraft: { name: 'draft', repo: '', connectionRef: activeConnection.name, visibility: 'private', description: '', autoInit: true },
    })
    await settle()
    const retry = findNode(root, node => node.type === 'button' && textContent(node) === 'Check again')!
    await (retry.props.onClick as () => Promise<void>)()
    await settle()
    expect(textContent(findNode(root, node => node.props.id === 'code-repository-connection')!)).toContain('github-prod')
    expect(textContent(root)).not.toContain('The new connection is not available yet')
    app.unmount()
  })

  it('does not let an older connection read overwrite a submit preflight', async () => {
    const stale = deferred<typeof activeConnection[]>()
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([activeConnection]).mockReturnValueOnce(stale.promise).mockResolvedValueOnce([])
    const { app, root, contextGeneration } = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'test'  })
    await settle()
    setInput(root, 'my-service', 'draft')
    contextGeneration.value += 1
    await settle()
    await submit(root)
    await settle()
    expect(textContent(root)).toContain('is no longer active')
    stale.resolve([activeConnection])
    await settle()
    expect(textContent(root)).toContain('no available GitHub connections')
    await submit(root)
    await settle()
    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('Select a GitHub connection.')
    app.unmount()
  })

  it('describes token inputs with a short hint and offers both permission models', async () => {
    const { app, root } = mount(ConnectionCreateView, {
      method: 'token', deletions: createResourceDeletions(), recoveryScope: 'test' ,
    })
    await settle()
    const input = findNode(root, node => node.props.id === 'code-connection-token')
    expect(input?.props['aria-describedby']).toBe('code-connection-token-hint')
    const permissions = findNode(root, node => node.type === 'details')
    expect(permissions).toBeDefined()
    expect(permissions?.props.open).toBeUndefined()
    expect(textContent(permissions!)).toContain('Token permissions')
    expect(textContent(permissions!)).toContain('fine-grained token')
    expect(textContent(permissions!)).toContain('Contents: read and write')
    expect(textContent(permissions!)).toContain('Workflows: read and write')
    expect(textContent(permissions!)).toContain('For a classic token')
    await submit(root)
    expect(input?.props['aria-describedby']).toBe('code-connection-token-error code-connection-token-hint')
    app.unmount()
  })

  it('restores scoped recovery without storing credentials and does not clear it on a late result', async () => {
    const pending = deferred<{ name: string }>()
    mocks.api.connect.mockReset().mockReturnValue(pending.promise)
    let view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'scope-a' })
    await settle()
    setInput(view.root, 'my-github', 'new-connection')
    setInput(view.root, 'acme', 'private-owner')
    setInput(view.root, 'ghp_…', 'private-token')
    const submission = submit(view.root)
    await settle()
    const key = 'railgrid:code:connection-attempt:scope-a'
    const record = window.sessionStorage.getItem(key)!
    expect(record).toContain('new-connection')
    expect(record).not.toContain('private-token')
    expect(record).not.toContain('private-owner')
    view.app.unmount()
    const other = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'scope-b' })
    await settle()
    expect(textContent(other.root)).not.toContain('could not be confirmed')
    other.app.unmount()
    view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'scope-a' })
    await settle()
    expect(textContent(view.root)).toContain('could not be confirmed')
    pending.resolve({ name: 'new-connection' })
    await submission
    expect(window.sessionStorage.getItem(key)).toBe(record)
    mocks.api.getConnection.mockResolvedValue({ name: 'new-connection', owner: 'private-owner', validated: false })
    const inspect = findNode(view.root, node => node.type === 'button' && textContent(node).includes('Inspect connection'))!
    await (inspect.props.onClick as () => Promise<void>)()
    await settle()
    const acknowledge = findNode(view.root, node => node.type === 'button' && textContent(node).includes('allow another request'))!
    ;(acknowledge.props.onClick as () => void)()
    expect(window.sessionStorage.getItem(key)).toBeNull()
    view.app.unmount()
  })

  it('does not remove a newer recovery record when an old write finishes', async () => {
    const pending = deferred<{ name: string }>()
    mocks.api.connect.mockReset().mockReturnValue(pending.promise)
    const view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'scope-a' })
    await settle()
    setInput(view.root, 'my-github', 'new-connection')
    setInput(view.root, 'acme', 'owner')
    setInput(view.root, 'ghp_…', 'secret')
    const submission = submit(view.root)
    await settle()
    const key = 'railgrid:code:connection-attempt:scope-a'
    const newer = JSON.stringify({ name: 'newer', id: 'other' })
    window.sessionStorage.setItem(key, newer)
    pending.resolve({ name: 'new-connection' })
    await submission
    expect(window.sessionStorage.getItem(key)).toBe(newer)
    expect(textContent(view.root)).toContain('recovery storage is unavailable or changed')
    view.app.unmount()
  })

  it('fails closed when recovery storage is unavailable', async () => {
    vi.spyOn(window.sessionStorage, 'setItem').mockImplementation(() => { throw new Error('unavailable') })
    const view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(view.root, 'my-github', 'new-connection')
    setInput(view.root, 'acme', 'owner')
    setInput(view.root, 'ghp_…', 'secret')
    mocks.api.connect.mockReset()
    await submit(view.root)
    expect(mocks.api.connect).not.toHaveBeenCalled()
    expect(textContent(view.root)).toContain('recovery storage is unavailable')
    view.app.unmount()
    vi.restoreAllMocks()
  })

  it.each(['secret write failed', 'malformed successful response', 'network response lost'])('blocks a blind retry after an ambiguous mutation: %s', async (failure) => {
    mocks.api.connect.mockReset().mockRejectedValue(new Error(failure))
    mocks.api.getConnection.mockResolvedValue({ name: 'new-connection', owner: 'octocat', validated: false })
    const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'New Connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    await submit(root)
    await settle()
    expect(textContent(root)).toContain('Setup may be incomplete')
    expect(textContent(root)).not.toContain('secret write failed')
    await submit(root)
    expect(mocks.api.connect).toHaveBeenCalledTimes(1)
    const inspect = findNode(root, node => node.type === 'button' && textContent(node).includes('Inspect connection'))!
    await (inspect.props.onClick as () => Promise<void>)()
    await settle()
    expect(mocks.api.getConnection).toHaveBeenCalledWith('new-connection')
    expect(textContent(root)).toContain('name does not establish')
    app.unmount()
  })

  it('keeps preflight failures retryable without claiming a write was sent', async () => {
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([]).mockRejectedValueOnce(new Error('read unavailable')).mockResolvedValue([])
    mocks.api.connect.mockReset().mockResolvedValue({ name: 'new-connection' })
    const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    await submit(root)
    await settle()
    expect(textContent(root)).toContain('read unavailable')
    expect(textContent(root)).not.toContain('Setup may be incomplete')
    await submit(root)
    expect(mocks.api.connect).toHaveBeenCalledTimes(1)
    app.unmount()
  })

  it('does not show an inspection result from a previous authority', async () => {
    const pending = deferred<{ name: string; owner: string; validated: boolean }>()
    mocks.api.connect.mockReset().mockRejectedValue(new Error('lost'))
    mocks.api.getConnection.mockReturnValue(pending.promise)
    const { app, root, contextGeneration } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    await submit(root)
    await settle()
    const inspect = findNode(root, node => node.type === 'button' && textContent(node).includes('Inspect connection'))!
    const checking = (inspect.props.onClick as () => Promise<void>)()
    contextGeneration.value++
    pending.resolve({ name: 'new-connection', owner: 'old-owner', validated: true })
    await checking
    await settle()
    expect(textContent(root)).not.toContain('old-owner')
    expect(textContent(root)).toContain('Setup may be incomplete')
    mocks.api.getConnection.mockResolvedValue({ name: 'new-connection', owner: 'current-owner', validated: false })
    await (inspect.props.onClick as () => Promise<void>)()
    await settle()
    expect(textContent(root)).toContain('current-owner')
    app.unmount()
  })

  it('offers recovery when the actual connection transport never settles', async () => {
    vi.useFakeTimers()
    try {
      const actual = await vi.importActual<typeof import('./api')>('./api')
      const transport = vi.fn(() => new Promise<Response>(() => {}))
      actual.setAPIContext({ tenant: 'timeout-view', token: 'test-token', fetch: transport })
      mocks.api.connect.mockReset().mockImplementation(actual.api.connect)
      const view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
      await settle()
      setInput(view.root, 'my-github', 'new-connection')
      setInput(view.root, 'acme', 'owner')
      setInput(view.root, 'ghp_…', 'secret')
      const submission = submit(view.root)
      await settle()
      await vi.advanceTimersByTimeAsync(15_000)
      await submission
      await settle()
      expect(transport).toHaveBeenCalledTimes(1)
      expect(textContent(view.root)).toContain('could not be confirmed')
      expect(findNode(view.root, node => node.type === 'button' && textContent(node).includes('Inspect connection'))).toBeDefined()
      view.app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('bounds initial connection reads and ignores a late result', async () => {
    vi.useFakeTimers()
    try {
      const pending = deferred<typeof existingConnection[]>()
      mocks.api.listConnections.mockReset().mockReturnValueOnce(pending.promise).mockResolvedValue([])
      const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
      await vi.advanceTimersByTimeAsync(15_000)
      await settle()
      expect(textContent(root)).toContain('timed out')
      pending.resolve([existingConnection])
      await settle()
      expect(textContent(root)).toContain('timed out')
      app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('locks the draft during preflight and unlocks it when the read fails', async () => {
    const preflight = deferred<typeof existingConnection[]>()
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(preflight.promise)
    const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    const fields = () => allNodes(root).filter(node => node.type === 'input')
    expect(fields()).toHaveLength(4)
    expect(fields().every(field => !field.props.readonly)).toBe(true)
    const submission = submit(root)
    await settle()
    expect(fields().every(field => field.props.readonly === true)).toBe(true)
    preflight.reject(new Error('Read failed'))
    await submission
    await settle()
    expect(textContent(root)).toContain('Read failed')
    expect(fields().every(field => !field.props.readonly)).toBe(true)
    expect(mocks.api.connect).not.toHaveBeenCalled()
    app.unmount()
  })

  it('keeps the draft locked through connection creation and unlocks it for recovery', async () => {
    const mutation = deferred<{ name: string }>()
    mocks.api.connect.mockReset().mockReturnValueOnce(mutation.promise)
    const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    setInput(root, 'https://github.example.com/api/v3', 'https://example.com/api/v3')
    const submission = submit(root)
    await settle()
    const fields = () => allNodes(root).filter(node => node.type === 'input')
    expect(fields().every(field => field.props.readonly === true)).toBe(true)
    expect(mocks.api.connect).toHaveBeenCalledWith(expect.objectContaining({ name: 'new-connection', owner: 'octocat', token: 'secret', baseURL: 'https://example.com/api/v3' }))
    mutation.reject(new Error('Response lost'))
    await submission
    await settle()
    expect(textContent(root)).toContain('could not be confirmed')
    expect(fields().every(field => !field.props.readonly)).toBe(true)
    app.unmount()
  })

  it('bounds a stalled preflight without issuing a write', async () => {
    vi.useFakeTimers()
    try {
      const pending = deferred<typeof existingConnection[]>()
      mocks.api.listConnections.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(pending.promise)
      mocks.api.connect.mockReset()
      const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
      await settle()
      setInput(root, 'my-github', 'new-connection')
      setInput(root, 'acme', 'octocat')
      setInput(root, 'ghp_…', 'secret')
      const submitting = submit(root)
      await vi.advanceTimersByTimeAsync(15_000)
      await submitting
      await settle()
      expect(textContent(root)).toContain('timed out')
      pending.resolve([])
      await settle()
      expect(mocks.api.connect).not.toHaveBeenCalled()
      app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('bounds a stalled inspection and ignores its late result', async () => {
    vi.useFakeTimers()
    try {
      const pending = deferred<{ name: string; owner: string; validated: boolean }>()
      mocks.api.connect.mockReset().mockRejectedValue(new Error('lost'))
      mocks.api.getConnection.mockReturnValue(pending.promise)
      const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
      await settle()
      setInput(root, 'my-github', 'new-connection')
      setInput(root, 'acme', 'octocat')
      setInput(root, 'ghp_…', 'secret')
      await submit(root)
      await settle()
      const inspect = findNode(root, node => node.type === 'button' && textContent(node).includes('Inspect connection'))!
      const checking = (inspect.props.onClick as () => Promise<void>)()
      await vi.advanceTimersByTimeAsync(15_000)
      await checking
      await settle()
      expect(textContent(root)).toContain('Could not inspect')
      pending.resolve({ name: 'new-connection', owner: 'late-owner', validated: true })
      await settle()
      expect(textContent(root)).not.toContain('late-owner')
      app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('shows a verified first-write rejection and clears only its own recovery record', async () => {
    const actual = await vi.importActual<typeof import('./api')>('./api')
    actual.setAPIContext({ tenant: 'test', token: 'test', fetch: async () => new Response(JSON.stringify({ kind: 'Status', apiVersion: 'v1', status: 'Failure', reason: 'Forbidden', message: 'Connection create denied', code: 403 }), { status: 403 }) })
    const error = await actual.api.connect({ name: 'demo', owner: 'owner', token: 'secret' }).catch(value => value)
    expect(actual.isRejectedConnectionWrite(error)).toBe(true)
    mocks.api.connect.mockReset().mockRejectedValue(error)
    const view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'rejected' })
    await settle()
    setInput(view.root, 'my-github', 'new-connection')
    setInput(view.root, 'acme', 'owner')
    setInput(view.root, 'ghp_…', 'secret')
    await submit(view.root)
    expect(textContent(view.root)).toContain('Connection create denied')
    expect(textContent(view.root)).not.toContain('could not be confirmed')
    expect(window.sessionStorage.getItem('railgrid:code:connection-attempt:rejected')).toBeNull()
    view.app.unmount()
  })

  it('does not clear a newer recovery record on a late rejected write', async () => {
    const actual = await vi.importActual<typeof import('./api')>('./api')
    actual.setAPIContext({ tenant: 'test', token: 'test', fetch: async () => new Response('{}', { status: 403 }) })
    const error = await actual.api.connect({ name: 'demo', owner: 'owner', token: 'secret' }).catch(value => value)
    const pending = deferred<never>()
    mocks.api.connect.mockReset().mockReturnValue(pending.promise)
    const view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'late-rejected' })
    await settle()
    setInput(view.root, 'my-github', 'new-connection')
    setInput(view.root, 'acme', 'owner')
    setInput(view.root, 'ghp_…', 'secret')
    const submission = submit(view.root)
    await settle()
    const key = 'railgrid:code:connection-attempt:late-rejected'
    const newer = JSON.stringify({ name: 'newer', id: 'other' })
    window.sessionStorage.setItem(key, newer)
    pending.reject(error)
    await submission
    expect(window.sessionStorage.getItem(key)).toBe(newer)
    expect(textContent(view.root)).toContain('recovery storage is unavailable or changed')
    view.app.unmount()
  })

  it.each([false, true])('settles a rejected request after unmount without deleting a newer record: %s', async newer => {
    const actual = await vi.importActual<typeof import('./api')>('./api')
    actual.setAPIContext({ tenant: 'test', token: 'test', fetch: async () => new Response('{}', { status: 403 }) })
    const error = await actual.api.connect({ name: 'demo', owner: 'owner', token: 'secret' }).catch(value => value)
    const pending = deferred<never>()
    mocks.api.connect.mockReset().mockReturnValue(pending.promise)
    const view = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'unmounted-rejection' })
    await settle()
    setInput(view.root, 'my-github', 'new-connection')
    setInput(view.root, 'acme', 'owner')
    setInput(view.root, 'ghp_…', 'secret')
    const submission = submit(view.root)
    await settle()
    const key = 'railgrid:code:connection-attempt:unmounted-rejection'
    expect(window.sessionStorage.getItem(key)).not.toBeNull()
    view.app.unmount()
    const nextRecord = JSON.stringify({ name: 'newer', id: 'other' })
    if (newer) window.sessionStorage.setItem(key, nextRecord)
    pending.reject(error)
    await submission
    expect(window.sessionStorage.getItem(key)).toBe(newer ? nextRecord : null)
  })

  it('allows a remounted view to acknowledge an attempt settled by the original view', async () => {
    const actual = await vi.importActual<typeof import('./api')>('./api')
    actual.setAPIContext({ tenant: 'test', token: 'test', fetch: async () => new Response('{}', { status: 403 }) })
    const error = await actual.api.connect({ name: 'demo', owner: 'owner', token: 'secret' }).catch(value => value)
    const pending = deferred<never>()
    mocks.api.connect.mockReset().mockReturnValue(pending.promise)
    const first = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'remount-rejected' })
    await settle()
    setInput(first.root, 'my-github', 'new-connection')
    setInput(first.root, 'acme', 'owner')
    setInput(first.root, 'ghp_…', 'secret')
    const submission = submit(first.root)
    await settle()
    first.app.unmount()
    const second = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions(), recoveryScope: 'remount-rejected' })
    await settle()
    expect(textContent(second.root)).toContain('could not be confirmed')
    pending.reject(error)
    await submission
    expect(window.sessionStorage.getItem('railgrid:code:connection-attempt:remount-rejected')).toBeNull()
    mocks.api.getConnection.mockResolvedValue({ name: 'new-connection', owner: 'owner', validated: false })
    const inspect = findNode(second.root, node => node.type === 'button' && textContent(node).includes('Inspect connection'))!
    await (inspect.props.onClick as () => Promise<void>)()
    await settle()
    const acknowledge = findNode(second.root, node => node.type === 'button' && textContent(node).includes('allow another request'))!
    ;(acknowledge.props.onClick as () => void)()
    await settle()
    expect(textContent(second.root)).not.toContain('could not be confirmed')
    expect(textContent(second.root)).not.toContain('recovery storage is unavailable or changed')
    second.app.unmount()
  })

  it('shows a pre-dispatch failure without marking the outcome uncertain', async () => {
    mocks.api.listConnections.mockReset().mockResolvedValue([])
    const actual = await vi.importActual<typeof import('./api')>('./api')
    vi.stubGlobal('crypto', undefined)
    const error = await actual.api.connect({ name: 'demo', owner: 'owner', token: 'secret' }).catch(value => value)
    vi.unstubAllGlobals()
    mocks.api.connect.mockReset().mockRejectedValue(error)
    const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    await submit(root)
    expect(textContent(root)).toContain('No connection was created.')
    expect(textContent(root)).not.toContain('could not be confirmed')
    app.unmount()
  })

  it('clears a verified pre-dispatch error after the context changes', async () => {
    const actual = await vi.importActual<typeof import('./api')>('./api')
    vi.stubGlobal('crypto', undefined)
    const error = await actual.api.connect({ name: 'demo', owner: 'owner', token: 'secret' }).catch(value => value)
    vi.unstubAllGlobals()
    const pending = deferred<never>()
    mocks.api.connect.mockReset().mockReturnValue(pending.promise)
    const { app, root, contextGeneration } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    const submission = submit(root)
    await settle()
    contextGeneration.value++
    pending.reject(error)
    await submission
    await settle()
    expect(textContent(root)).not.toContain('could not be confirmed')
    app.unmount()
  })

  it('retains uncertainty for a forged pre-dispatch transport error', async () => {
    mocks.api.connect.mockReset().mockRejectedValue({ reason: 'PreDispatch', message: 'forged' })
    const { app, root } = mount(ConnectionCreateView, { method: 'token', deletions: createResourceDeletions() })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'secret')
    await submit(root)
    expect(textContent(root)).toContain('could not be confirmed')
    app.unmount()
  })

  it('re-reads before mutation and refuses an active normalized name collision', async () => {
    mocks.api.listConnections.mockReset()
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([existingConnection])
    mocks.api.connect.mockReset()

    const created = vi.fn()
    const { app, root } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      onCreated: created,
    })
    await settle()
    setInput(root, 'my-github', ' GitHub-Prod ')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'new-secret')

    await submit(root)
    expect(mocks.api.listConnections).toHaveBeenCalledTimes(2)
    expect(mocks.api.connect).not.toHaveBeenCalled()
    expect(created).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('Connection "github-prod" already exists.')
    app.unmount()
  })

  it('keeps a tombstoned normalized name reserved until the resource disappears', async () => {
    const tombstone = { ...existingConnection, deletionTimestamp: '2026-08-27T19:00:00Z' }
    mocks.api.listConnections.mockReset()
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([tombstone])
    mocks.api.connect.mockReset()

    const { app, root } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
    })
    await settle()
    setInput(root, 'my-github', ' GitHub-Prod ')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'new-secret')

    await submit(root)
    expect(mocks.api.connect).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('Connection "github-prod" is still deleting.')
    app.unmount()
  })

  it('keeps an acknowledged delete reserved while the same UID remains visible', async () => {
    const deletions = createResourceDeletions()
    deletions.acknowledge('connection', existingConnection.name, existingConnection.uid)
    mocks.api.listConnections.mockReset().mockResolvedValue([existingConnection])
    mocks.api.connect.mockReset()

    const { app, root } = mount(ConnectionCreateView, {
      method: 'token',
      deletions,
    })
    await settle()
    setInput(root, 'my-github', ' GitHub-Prod ')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'new-secret')

    await submit(root)
    expect(mocks.api.connect).not.toHaveBeenCalled()
    expect(textContent(root)).toContain('Connection "github-prod" is still deleting.')
    app.unmount()
  })

  it('rejects a stale preflight before calling the credential mutation', async () => {
    const preflight = deferred<typeof existingConnection[]>()
    mocks.api.listConnections.mockReset()
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(preflight.promise)
      .mockResolvedValue([])
    mocks.api.connect.mockReset()

    const created = vi.fn()
    const { app, root, contextGeneration } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      onCreated: created,
    })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'new-secret')
    const pendingSubmit = submit(root)
    await settle()

    // Model a shell context update before Vue has had a chance to flush the
    // keyed route unmount. The preflight result must not cross that boundary.
    contextGeneration.value += 1
    preflight.resolve([])
    await pendingSubmit
    await settle()

    expect(mocks.api.connect).not.toHaveBeenCalled()
    expect(mocks.toast).not.toHaveBeenCalled()
    expect(created).not.toHaveBeenCalled()
    expect(findNode(root, node => node.type === 'button' && node.props.type === 'submit')?.props.disabled).toBe(false)
    expect((findNode(root, node => node.props.placeholder === 'my-github') as (TreeNode & { value?: string }) | undefined)?.value).toBe('new-connection')
    app.unmount()
  })

  it('does not toast or navigate when connection creation resolves after the workspace changes', async () => {
    const mutation = deferred<{ name: string }>()
    mocks.api.connect.mockReturnValueOnce(mutation.promise)
    const created = vi.fn()
    const { app, root, contextGeneration } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      onCreated: created,
    })
    await settle()
    setInput(root, 'my-github', 'new-connection')
    setInput(root, 'acme', 'octocat')
    setInput(root, 'ghp_…', 'new-secret')

    const pendingSubmit = submit(root)
    await settle()
    contextGeneration.value += 1
    mutation.resolve({ name: 'new-connection' })
    await pendingSubmit
    await settle()

    expect(mocks.toast).not.toHaveBeenCalled()
    expect(created).not.toHaveBeenCalled()
    expect(findNode(root, node => node.type === 'button' && node.props.type === 'submit')?.props.disabled).toBe(true)
    expect(textContent(root)).toContain('Setup may be incomplete')
    app.unmount()
  })

  it('rejects a repository preflight that crosses the pre-unmount authority window', async () => {
    const preflight = deferred<typeof activeRepository[]>()
    mocks.api.listRepositories.mockReset()
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(preflight.promise)
      .mockResolvedValue([])
    mocks.api.listConnections.mockReset().mockResolvedValue([activeConnection])
    mocks.api.createRepository.mockReset()

    const created = vi.fn()
    const { app, root, contextGeneration } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(), recoveryScope: 'test' ,
      onCreated: created,
    })
    await settle()
    setInput(root, 'my-service', 'new-repository')
    const pendingSubmit = submit(root)
    await settle()

    // Model a shell context update before Vue has had a chance to flush the
    // keyed route unmount. The complete preflight result must not commit its
    // duplicate decision or cross into createRepository.
    contextGeneration.value += 1
    preflight.resolve([activeRepository])
    await pendingSubmit
    await settle()

    expect(mocks.api.createRepository).not.toHaveBeenCalled()
    expect(created).not.toHaveBeenCalled()
    expect(textContent(root)).not.toContain('already exists.')
    expect(findNode(root, node => node.type === 'button' && node.props.type === 'submit')?.props.disabled).toBe(false)
    expect((findNode(root, node => node.props.placeholder === 'my-service') as (TreeNode & { value?: string }) | undefined)?.value).toBe('new-repository')
    app.unmount()
  })

  it('uses the same shortened default GitHub name in the preview, request and recovery', async () => {
    const longName = 'a'.repeat(120)
    const remote = `octocat/${'a'.repeat(100)}`
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.createRepository.mockRejectedValueOnce(new Error('connection lost'))
    const view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'long-name' })
    await settle()
    setInput(view.root, 'my-service', longName)
    await settle()
    expect(textContent(view.root)).toContain(remote)
    expect(textContent(view.root)).toContain('shortened to 100 characters')
    await submit(view.root)
    await settle()
    expect(mocks.api.createRepository).toHaveBeenCalledWith(expect.objectContaining({ name: longName, repo: undefined }))
    expect(textContent(view.root)).toContain(remote)
    expect(textContent(view.root)).toContain('may have completed')
    view.app.unmount()
  })

  it('retains a lost request across remount and requires a successful inspection before acknowledgement', async () => {
    mocks.api.listConnections.mockResolvedValue([activeConnection])
    mocks.api.createRepository.mockRejectedValueOnce(new Error('connection lost'))
    let view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'recovery-test' })
    await settle()
    setInput(view.root, 'my-service', 'submitted-name')
    await submit(view.root)
    await settle()
    expect(textContent(view.root)).toContain('may have completed')
    expect(findNode(view.root, node => node.type === 'button' && textContent(node) === 'I checked, allow another request')).toBeUndefined()
    view.app.unmount()

    view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'recovery-test' })
    await settle()
    expect(textContent(view.root)).toContain('submitted-name')
    expect(textContent(view.root)).toContain('may have completed')
    mocks.api.listRepositories.mockRejectedValueOnce(new Error('offline'))
    let check = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Check repositories')!
    ;(check.props.onClick as () => void)()
    await settle()
    expect(textContent(view.root)).toContain('Could not check repositories')
    expect(findNode(view.root, node => node.type === 'button' && textContent(node) === 'I checked, allow another request')).toBeUndefined()
    check = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Check repositories')!
    ;(check.props.onClick as () => void)()
    await settle()
    expect(textContent(view.root)).toContain('may still complete later')
    const acknowledge = findNode(view.root, node => node.type === 'button' && textContent(node) === 'I checked, allow another request')!
    ;(acknowledge.props.onClick as () => void)()
    await settle()
    expect(textContent(view.root)).not.toContain('may have completed')
    view.app.unmount()
  })

  it('times out a stalled create and ignores its late success', async () => {
    vi.useFakeTimers()
    try {
      const result = deferred<typeof activeRepository>()
      const created = vi.fn()
      mocks.api.listConnections.mockResolvedValue([activeConnection])
      mocks.api.createRepository.mockReturnValueOnce(result.promise)
      const view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'timeout', onCreated: created })
      await settle()
      setInput(view.root, 'my-service', 'stalled')
      const pending = submit(view.root)
      await settle()
      await vi.advanceTimersByTimeAsync(15000)
      await pending
      await settle()
      expect(textContent(view.root)).toContain('may have completed')
      result.resolve(activeRepository)
      await settle()
      expect(created).not.toHaveBeenCalled()
      expect(textContent(view.root)).toContain('may have completed')
      view.app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('bounds stalled initial reads and allows a fresh retry without accepting late results', async () => {
    vi.useFakeTimers()
    try {
      const stalled = deferred<typeof activeRepository[]>()
      mocks.api.listRepositories.mockReset().mockReturnValueOnce(stalled.promise).mockResolvedValue([])
      mocks.api.listConnections.mockResolvedValue([activeConnection])
      const view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'initial-timeout' })
      await settle()
      await vi.advanceTimersByTimeAsync(15000)
      await settle()
      expect(textContent(view.root)).toContain('timed out')
      const retry = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Retry repositories')!
      ;(retry.props.onClick as () => void)()
      await settle()
      stalled.resolve([activeRepository])
      await settle()
      expect(textContent(view.root)).not.toContain('timed out')
      expect(findNode(view.root, node => node.type === 'button' && node.props.type === 'submit')).toBeDefined()
      view.app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('bounds a stalled preflight and preserves the draft without issuing a create', async () => {
    vi.useFakeTimers()
    try {
      const stalled = deferred<typeof activeRepository[]>()
      mocks.api.listRepositories.mockReset().mockResolvedValueOnce([]).mockReturnValueOnce(stalled.promise)
      mocks.api.listConnections.mockResolvedValue([activeConnection])
      const view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'preflight-timeout' })
      await settle()
      setInput(view.root, 'my-service', 'saved-draft')
      const pending = submit(view.root)
      await settle()
      await vi.advanceTimersByTimeAsync(15000)
      await pending
      await settle()
      expect(textContent(view.root)).toContain('timed out')
      expect(mocks.api.createRepository).not.toHaveBeenCalled()
      stalled.resolve([activeRepository])
      await settle()
      expect((findNode(view.root, node => node.props.placeholder === 'my-service') as (TreeNode & { value?: string }) | undefined)?.value).toBe('saved-draft')
      expect(mocks.api.createRepository).not.toHaveBeenCalled()
      view.app.unmount()
    } finally { vi.useRealTimers() }
  })

  it('ignores a repository retry that finishes after the submission preflight', async () => {
    const late = deferred<typeof activeRepository[]>()
    const connectionPreflight = deferred<typeof activeConnection[]>()
    mocks.api.listRepositories.mockReset().mockResolvedValueOnce([]).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce([]).mockReturnValueOnce(late.promise)
    mocks.api.listConnections.mockReset().mockResolvedValueOnce([activeConnection]).mockResolvedValueOnce([activeConnection]).mockReturnValueOnce(connectionPreflight.promise)
    const deletions = createResourceDeletions()
    const reconcile = vi.spyOn(deletions, 'reconcile')
    const view = mount(RepositoryCreateView, { deletions, recoveryScope: 'late-retry' })
    await settle()
    view.contextGeneration.value += 1
    await settle()
    setInput(view.root, 'my-service', 'new-repository')
    const pending = submit(view.root)
    await settle()
    const retry = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Retry repositories')!
    ;(retry.props.onClick as () => void)()
    await settle()
    connectionPreflight.resolve([activeConnection])
    await pending
    await settle()
    const calls = reconcile.mock.calls.length
    late.resolve([activeRepository])
    await settle()
    expect(reconcile.mock.calls.length).toBe(calls)
    view.app.unmount()
  })

  it('bounds inspection and resets it on authority change without accepting stale results', async () => {
    vi.useFakeTimers()
    try {
      mocks.api.listConnections.mockResolvedValue([activeConnection])
      mocks.api.createRepository.mockRejectedValueOnce(new Error('lost'))
      const view = mount(RepositoryCreateView, { deletions: createResourceDeletions(), recoveryScope: 'inspection' })
      await settle()
      setInput(view.root, 'my-service', 'stalled')
      await submit(view.root)
      await settle()
      const first = deferred<typeof activeRepository[]>()
      mocks.api.listRepositories.mockReturnValueOnce(first.promise)
      let check = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Check repositories')!
      ;(check.props.onClick as () => void)()
      await settle()
      await vi.advanceTimersByTimeAsync(15000)
      await settle()
      expect(textContent(view.root)).toContain('Could not check repositories')
      first.resolve([])
      await settle()
      expect(textContent(view.root)).not.toContain('may still complete later')
      const stale = deferred<typeof activeRepository[]>()
      mocks.api.listRepositories.mockReturnValueOnce(stale.promise)
      check = findNode(view.root, node => node.type === 'button' && textContent(node) === 'Check repositories')!
      ;(check.props.onClick as () => void)()
      await settle()
      view.contextGeneration.value += 1
      await settle()
      expect(findNode(view.root, node => node.type === 'button' && textContent(node) === 'Check repositories')?.props.disabled).toBe(false)
      stale.resolve([])
      await settle()
      expect(findNode(view.root, node => node.type === 'button' && textContent(node) === 'I checked, allow another request')).toBeUndefined()
      view.app.unmount()
    } finally { vi.useRealTimers() }
  })

})
