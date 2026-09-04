import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h, KeepAlive, nextTick, ref, ssrContextKey, type Component, type RendererOptions } from '@vue/runtime-core'
import * as VueRuntime from 'vue'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import type { VNode } from 'vue'

import { createResourceDeletions } from './refresh'
import { contextGenerationKey } from './context'

const mocks = vi.hoisted(() => ({
  api: {
    listConnections: vi.fn(),
    listRepositories: vi.fn(),
    oauthConfig: vi.fn(),
    connect: vi.fn(),
    createRepository: vi.fn(),
  },
}))

vi.mock('./api', () => ({
  api: mocks.api,
  normalizeResourceName: (value: string) => value.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 253) || 'x',
}))
vi.mock('./portalkit/confirm', () => ({ confirmDialog: vi.fn() }))
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
vi.mock('./portalkit/StatusBadge.vue', async () => {
  const { h } = await import('vue')
  return { default: { setup: () => () => h('span', 'status') } }
})
vi.mock('./portalkit/FirstRunGuide.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      props: { title: String, primaryLabel: String },
      setup: props => () => h('section', { class: 'k-first-run' }, [
        h('h3', props.title),
        h('button', props.primaryLabel),
      ]),
    }),
  }
})
vi.mock('lucide-vue-next', async () => {
  const { h } = await import('vue')
  const icon = { setup: () => () => h('span', { 'aria-hidden': 'true' }) }
  return { ArrowLeft: icon, ArrowRight: icon, Check: icon, CircleDot: icon, Link2: icon }
})

import ConnectionsView from './views/ConnectionsView.vue'
import ConnectionCreateView from './views/ConnectionCreateView.vue'
import RepositoryCreateView from './views/RepositoryCreateView.vue'
import connectionsSource from './views/ConnectionsView.vue?raw'
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
    compilerOptions: { bindingMetadata: script.bindings },
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

;(ConnectionsView as unknown as { render: unknown }).render = compileClientRender(connectionsSource, 'ConnectionsView.vue')
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
  const app = renderer.createApp(component, props)
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
      setInterval: () => 0,
      clearInterval: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
    },
  })
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.listRepositories.mockResolvedValue([])
  mocks.api.oauthConfig.mockResolvedValue({ enabled: false })
  mocks.api.connect.mockResolvedValue({ name: 'created' })
  mocks.api.createRepository.mockResolvedValue({ name: 'created-repository' })
})

afterEach(() => {
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

  it('shows a retry action on the GitHub creation route when configuration fails', async () => {
    const configRequest = deferred<{ enabled: boolean }>()
    mocks.api.oauthConfig.mockReturnValue(configRequest.promise)
    const { app, root } = mount(ConnectionCreateView, {
      method: 'github',
      deletions: createResourceDeletions(),
    })
    await settle()

    configRequest.reject(new Error('oauth backend unavailable'))
    await settle()

    expect(textContent(root)).toContain('GitHub sign-in configuration could not be loaded: oauth backend unavailable')
    expect(textContent(root)).toContain('Retry GitHub sign-in')
    expect(findNode(root, node => node.props.role === 'alert')).toBeDefined()
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

  it('reports required connection fields and focuses the first invalid input', async () => {
    mocks.api.listConnections.mockResolvedValue([])
    const { app, root } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(),
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

  it('reports required repository fields and focuses the first invalid control', async () => {
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listRepositories.mockResolvedValue([])
    const { app, root } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(),
    })
    await settle()

    await submit(root)
    await settle()

    expect(textContent(root)).toContain('Select an active connection.')
    expect(textContent(root)).toContain('Enter an object name.')
    const first = findNode(root, node => node.type === 'select' && node.props.id === 'code-repository-connection')
    expect(first?.props['aria-invalid']).toBe('true')
    expect(first?.props.focused).toBe(true)
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
      deletions: createResourceDeletions(),
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
      deletions: createResourceDeletions(),
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
    mocks.api.connect.mockReset()

    const created = vi.fn()
    const { app, root, contextGeneration } = mount(ConnectionCreateView, {
      method: 'token',
      deletions: createResourceDeletions(),
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
    expect(created).not.toHaveBeenCalled()
    app.unmount()
  })

  it('rejects a repository preflight that crosses the pre-unmount authority window', async () => {
    const preflight = deferred<typeof activeRepository[]>()
    mocks.api.listRepositories.mockReset()
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(preflight.promise)
    mocks.api.listConnections.mockReset().mockResolvedValue([activeConnection])
    mocks.api.createRepository.mockReset()

    const created = vi.fn()
    const { app, root, contextGeneration } = mount(RepositoryCreateView, {
      deletions: createResourceDeletions(),
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
    app.unmount()
  })
})
