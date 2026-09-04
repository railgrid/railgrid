import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, nextTick, ssrContextKey, type RendererOptions } from '@vue/runtime-core'
import * as VueRuntime from 'vue'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import type { VNode } from 'vue'

import type { Collaborator, Connection, ConnectionDetail, DeployKey, KubernetesListPage, Package, RepositoryDetail } from './types'
import { createResourceDeletions } from './refresh'

const mocks = vi.hoisted(() => ({
  api: {
    getRepository: vi.fn(),
    listConnections: vi.fn(),
    listDeployKeys: vi.fn(),
    listCollaborators: vi.fn(),
    listPackagesPage: vi.fn(),
    listPackages: vi.fn(),
    deleteRepository: vi.fn(),
    getConnection: vi.fn(),
    deleteConnection: vi.fn(),
  },
  confirmDialog: vi.fn(),
}))

vi.mock('./api', () => ({ api: mocks.api }))
vi.mock('./portalkit/confirm', () => ({ confirmDialog: mocks.confirmDialog }))
vi.mock('./portalkit/ActionMenu.vue', async () => {
  const { h, ref } = await import('vue')
  type ActionItem = { id: string; label: string; disabled?: boolean; busy?: boolean }
  return {
    default: {
      props: ['label', 'items', 'disabled'],
      emits: ['select'],
      setup: (props: { label: string; items: readonly ActionItem[]; disabled?: boolean }, { emit }: { emit: (event: string, id: string) => void }) => {
        const open = ref(false)
        const toggle = () => {
          if (!props.disabled) open.value = !open.value
        }
        const select = (item: ActionItem) => {
          if (props.disabled || item.disabled || item.busy) return
          open.value = false
          emit('select', item.id)
        }
        return () => h('div', { class: 'action-menu-stub' }, [
          h('button', { type: 'button', 'aria-label': props.label, disabled: props.disabled, onClick: toggle }, props.label),
          open.value ? h('div', { role: 'menu' }, props.items.map(item => h('button', {
            type: 'button',
            disabled: item.disabled || item.busy,
            'aria-busy': item.busy ? 'true' : undefined,
            onClick: () => select(item),
          }, item.label))) : null,
        ])
      },
    },
  }
})
vi.mock('./portalkit/ConditionsPanel.vue', async () => {
  const { h } = await import('vue')
  return { default: { setup: () => () => h('div', { class: 'conditions-stub' }) } }
})
vi.mock('./portalkit/ResourcePage.vue', async () => {
  const { h } = await import('vue')
  type StubSlots = Record<string, () => VNode[]>
  return {
    default: {
      props: ['title', 'kind', 'loaded', 'loading', 'error', 'stale'],
      setup: (props: { title: string }, { slots }: { slots: StubSlots }) => () => h('section', { class: 'resource-page-stub' }, [
        h('h1', props.title),
        slots.meta?.(),
       slots.status?.(),
        slots.actions?.(),
        slots.summary?.(),
        slots.body?.(),
      ]),
    },
  }
})
vi.mock('./portalkit/ResourceSectionCard.vue', async () => {
  const { h } = await import('vue')
  type StubSlots = Record<string, () => VNode[]>
  return {
    default: {
      props: ['id', 'eyebrow', 'title', 'description'],
      setup: (_props: unknown, { slots }: { slots: StubSlots }) => () => h('section', { class: 'section-card-stub' }, [
        slots.actions?.(),
        slots.default?.(),
      ]),
    },
  }
})
vi.mock('./portalkit/ResourceStatCards.vue', async () => {
  const { h } = await import('vue')
  return {
    default: {
      props: ['cards'],
      setup: (props: { cards: Array<{ id: string; label: string; value: string | number }> }) => () =>
        h('div', { class: 'stat-cards-stub' }, props.cards.map(card =>
          h('div', { 'data-k-resource-stat-card': card.id }, `${card.label} ${card.value}`),
        )),
    },
  }
})
vi.mock('./portalkit/StatusBadge.vue', async () => {
  const { h } = await import('vue')
  return { default: { props: ['status'], setup: (props: { status: string }) => () => h('span', { class: 'status-badge-stub' }, props.status) } }
})
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
  return {
    default: {
      props: ['label', 'busy-label', 'busy', 'disabled'],
      setup: (props: { label: string; disabled: boolean }, { emit }: { emit: (event: string) => void }) => () => h('button', {
        disabled: props.disabled,
        onClick: () => emit('click'),
      }, props.label),
      emits: ['click'],
    },
  }
})

import RepoDetailView from './views/RepoDetailView.vue'
import ConnectionDetailView from './views/ConnectionDetailView.vue'
import repoSource from './views/RepoDetailView.vue?raw'
import connectionSource from './views/ConnectionDetailView.vue?raw'

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
  parent: TreeNode | null
  text?: string
  setAttribute(name: string, value: string): void
  removeAttribute(name: string): void
}

function createTreeElement(type: string): TreeNode {
  const node: TreeNode = {
    type,
    props: {},
    children: [],
    parent: null,
    setAttribute(name, value) {
      this.props[name] = value
    },
    removeAttribute(name) {
      delete this.props[name]
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
    if (anchor) {
      const index = parent.children.indexOf(anchor)
      parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
    } else {
      parent.children.push(node)
    }
  },
  remove(node) {
    if (!node.parent) return
    const index = node.parent.children.indexOf(node)
    if (index >= 0) node.parent.children.splice(index, 1)
    node.parent = null
  },
  createElement: type => createTreeElement(type),
  createText: text => ({ type: '#text', props: {}, children: [], parent: null, text, setAttribute() {}, removeAttribute() {} }),
  createComment: text => ({ type: '#comment', props: {}, children: [], parent: null, text, setAttribute() {}, removeAttribute() {} }),
  setText(node, text) {
    node.text = text
  },
  setElementText(node, text) {
    node.children = [{ type: '#text', props: {}, children: [], parent: node, text, setAttribute() {}, removeAttribute() {} }]
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

;(RepoDetailView as unknown as { render: unknown }).render = compileClientRender(repoSource, 'RepoDetailView.vue')
;(ConnectionDetailView as unknown as { render: unknown }).render = compileClientRender(connectionSource, 'ConnectionDetailView.vue')

function textContent(node: TreeNode): string {
  return node.text ?? node.children.map(textContent).join('')
}

function allNodes(root: TreeNode): TreeNode[] {
  return [root, ...root.children.flatMap(allNodes)]
}

function findNode(root: TreeNode, predicate: (node: TreeNode) => boolean): TreeNode | undefined {
  return allNodes(root).find(predicate)
}

function click(node: TreeNode): void {
  const handler = node.props.onClick
  if (typeof handler === 'function') handler({ currentTarget: node, target: node, preventDefault() {} })
}

async function settle(): Promise<void> {
  for (let index = 0; index < 4; index += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function mount(component: typeof RepoDetailView | typeof ConnectionDetailView, props: Record<string, unknown>) {
  const root = createTreeElement('root')
  const app = renderer.createApp(component, props)
  app.provide(ssrContextKey, { modules: new Set<string>() })
  app.mount(root)
  return { app, root }
}

const repository: RepositoryDetail = {
  name: 'orders',
  uid: 'orders-uid',
  connectionRef: 'github',
  repo: 'orders',
  owner: 'faros',
  visibility: 'private',
  ready: true,
  conditions: [],
}

const connection: ConnectionDetail = {
  name: 'github',
  uid: 'github-uid',
  provider: 'github',
  type: 'pat',
  owner: 'faros',
  secretName: 'github-token',
  scopes: ['repo'],
  validated: true,
  conditions: [],
}

const noPackages: Package[] = []

let previousWindow: unknown

beforeEach(() => {
  previousWindow = (globalThis as typeof globalThis & { window?: unknown }).window
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      setInterval: () => 0,
      clearInterval: () => {},
    },
  })
  mocks.confirmDialog.mockResolvedValue(true)
  mocks.api.getRepository.mockResolvedValue(repository)
  mocks.api.getConnection.mockResolvedValue(connection)
  mocks.api.deleteRepository.mockResolvedValue(undefined)
  mocks.api.deleteConnection.mockResolvedValue(undefined)
  mocks.api.listPackages.mockResolvedValue(noPackages)
})

afterEach(() => {
  vi.clearAllMocks()
  if (previousWindow === undefined) {
    Reflect.deleteProperty(globalThis, 'window')
  } else {
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
  }
})

describe('mounted resource deletion state', () => {
  it('shows a current-generation repository controller failure as failed', async () => {
    mocks.api.getRepository.mockResolvedValue({ ...repository, ready: false, failed: true, message: 'GitHub rejected the request.' })
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listDeployKeys.mockResolvedValue([])
    mocks.api.listCollaborators.mockResolvedValue([])
    mocks.api.listPackagesPage.mockResolvedValue({ items: [], continue: '', resourceVersion: '' })

    const { app, root } = mount(RepoDetailView, {
      name: repository.name,
      deletions: createResourceDeletions(),
    })
    await settle()

    expect(findNode(root, node => node.type === 'span' && node.props.class === 'status-badge-stub' && textContent(node) === 'failed')).toBeDefined()
    app.unmount()
  })

  it('keeps the repository snapshot visible and announces Deleting while a delete is pending, then recovers after failure', async () => {
    const connections = deferred<Connection[]>()
    const keys = deferred<DeployKey[]>()
    const collaborators = deferred<Collaborator[]>()
    const packages = deferred<KubernetesListPage<Package>>()
    const deleteRequest = deferred<void>()
    mocks.api.listConnections.mockReturnValue(connections.promise)
    mocks.api.listDeployKeys.mockReturnValue(keys.promise)
    mocks.api.listCollaborators.mockReturnValue(collaborators.promise)
    mocks.api.listPackagesPage.mockReturnValue(packages.promise)
    mocks.api.deleteRepository.mockReturnValue(deleteRequest.promise)

    const { app, root } = mount(RepoDetailView, {
      name: repository.name,
      deletions: createResourceDeletions(),
    })
    await settle()

    expect(mocks.api.listConnections).toHaveBeenCalled()
    expect(mocks.api.listDeployKeys).toHaveBeenCalled()
    expect(mocks.api.listCollaborators).toHaveBeenCalled()
    expect(mocks.api.listPackagesPage).toHaveBeenCalled()
    const refreshButton = findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Refresh')
    expect(refreshButton?.props.disabled).toBe(false)
    const menuTrigger = findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More repository actions')
    expect(menuTrigger?.props.disabled).toBe(false)
    click(menuTrigger!)
    await settle()
    const deleteButton = findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Delete repository')
    expect(deleteButton?.props.disabled).toBe(false)
    click(deleteButton!)
    await settle()

    expect(mocks.confirmDialog).toHaveBeenCalledWith({
      title: 'Delete repository "orders"?',
      message: 'This removes the repository on the git host. This cannot be undone.',
      confirmLabel: 'Delete',
      danger: true,
    })
    expect(findNode(root, node => node.props.role === 'menu')).toBeUndefined()
    expect(textContent(root)).toContain('orders')
    expect(textContent(root)).toContain('Deleting this repository.')
    expect(findNode(root, node => node.props.role === 'status' && node.props['aria-live'] === 'polite' && textContent(node).includes('Deleting this repository.'))).toBeDefined()
    expect(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More repository actions')?.props.disabled).toBe(true)

    deleteRequest.reject({ reason: 'GraphQLError', message: 'delete failed' })
    await settle()

    expect(textContent(root)).not.toContain('Deleting this repository.')
    expect(textContent(root)).toContain('GraphQLError: delete failed')
    expect(textContent(root)).toContain('orders')
    expect(findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Refresh')?.props.disabled).toBe(false)
    expect(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More repository actions')?.props.disabled).toBe(false)
    app.unmount()
  })

  it('keeps the connection snapshot and live deletion announcement while a delete is pending, then recovers after failure', async () => {
    const deleteRequest = deferred<void>()
    mocks.api.deleteConnection.mockReturnValue(deleteRequest.promise)

    const back = vi.fn()
    const { app, root } = mount(ConnectionDetailView, {
      name: connection.name,
      deletions: createResourceDeletions(),
      onBack: back,
    })
    await settle()

    const menuTrigger = findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')
    expect(menuTrigger?.props.disabled).toBe(false)
    click(menuTrigger!)
    await settle()
    const deleteButton = findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Delete connection')
    expect(deleteButton?.props.disabled).toBe(false)
    click(deleteButton!)
    await settle()

    expect(mocks.confirmDialog).toHaveBeenCalledWith({
      title: 'Delete connection "github"?',
      message: 'Repositories using it will stop reconciling.',
      confirmLabel: 'Delete',
      danger: true,
    })
    expect(findNode(root, node => node.props.role === 'menu')).toBeUndefined()
    expect(textContent(root)).toContain('github')
    expect(textContent(root)).toContain('Deleting this connection.')
    expect(findNode(root, node => node.props.role === 'status' && node.props['aria-live'] === 'polite' && textContent(node).includes('Deleting this connection.'))).toBeDefined()
    expect(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')?.props.disabled).toBe(true)

    deleteRequest.reject({ reason: 'GraphQLError', message: 'delete failed' })
    await settle()

    expect(textContent(root)).not.toContain('Deleting this connection.')
    expect(textContent(root)).toContain('GraphQLError: delete failed')
    expect(textContent(root)).toContain('github')
    expect(back).not.toHaveBeenCalled()
    expect(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')?.props.disabled).toBe(false)
    app.unmount()
  })

  it('closes the connection menu and leaves the resource actionable when deletion is cancelled', async () => {
    mocks.confirmDialog.mockResolvedValueOnce(false)
    const { app, root } = mount(ConnectionDetailView, {
      name: connection.name,
      deletions: createResourceDeletions(),
    })
    await settle()

    const menuTrigger = findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')
    expect(menuTrigger?.props.disabled).toBe(false)
    click(menuTrigger!)
    await settle()
    const deleteButton = findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Delete connection')
    click(deleteButton!)
    await settle()

    expect(mocks.confirmDialog).toHaveBeenCalledTimes(1)
    expect(mocks.api.deleteConnection).not.toHaveBeenCalled()
    expect(findNode(root, node => node.props.role === 'menu')).toBeUndefined()
    expect(textContent(root)).not.toContain('Deleting this connection.')
    expect(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')?.props.disabled).toBe(false)
    app.unmount()
  })

  it('omits unresolved login and scopes from the connection summary', async () => {
    mocks.api.getConnection.mockResolvedValue({ ...connection, login: undefined, scopes: [] })
    const { app, root } = mount(ConnectionDetailView, {
      name: connection.name,
      deletions: createResourceDeletions(),
    })
    await settle()

    expect(findNode(root, node => node.props['data-k-resource-stat-card'] === 'owner')).toBeDefined()
    expect(findNode(root, node => node.props['data-k-resource-stat-card'] === 'login')).toBeUndefined()
    expect(findNode(root, node => node.props['data-k-resource-stat-card'] === 'scopes')).toBeUndefined()
    expect(textContent(root)).not.toContain('Login')
    expect(textContent(root)).not.toContain('Scopes')
    app.unmount()
  })
})
