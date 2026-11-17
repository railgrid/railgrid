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
    listRepositories: vi.fn(),
    listConnections: vi.fn(),
    listDeployKeys: vi.fn(),
    listCollaborators: vi.fn(),
    listPackagesPage: vi.fn(),
    listPackages: vi.fn(),
    deleteRepository: vi.fn(),
    deleteDeployKey: vi.fn(),
    deleteCollaborator: vi.fn(),
    createCollaborator: vi.fn(),
    getConnection: vi.fn(),
    deleteConnection: vi.fn(),
  },
  confirmDialog: vi.fn(),
  toast: vi.fn(),
}))

vi.mock('./api', () => ({ api: mocks.api }))
vi.mock('./portalkit/confirm', () => ({ confirmDialog: mocks.confirmDialog }))
vi.mock('./portalkit/toast', () => ({ toast: mocks.toast }))
vi.mock('./portalkit/InlineNotification.vue', async () => {
  const { h } = await import('vue')
  return { default: { props: ['title', 'message', 'actionLabel', 'actionBusy', 'dismissible'], emits: ['action', 'dismiss'], setup: (props: { title: string; message: string; actionLabel: string; actionBusy: boolean; dismissible: boolean }, { emit }: { emit: (name: 'action' | 'dismiss') => void }) => () => h('div', [props.title, props.message, props.actionLabel ? h('button', { 'aria-busy': props.actionBusy ? 'true' : undefined, onClick: () => { if (!props.actionBusy) emit('action') } }, props.actionLabel) : null, props.dismissible ? h('button', { onClick: () => emit('dismiss') }, 'Dismiss') : null]) } }
})
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
import DashboardTile from './DashboardTile.vue'
import repoSource from './views/RepoDetailView.vue?raw'
import connectionSource from './views/ConnectionDetailView.vue?raw'
import dashboardSource from './DashboardTile.vue?raw'

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
  focus?(): void
  setAttribute(name: string, value: string): void
  removeAttribute(name: string): void
}

let focusedNode: TreeNode | null = null

function createTreeElement(type: string): TreeNode {
  const node: TreeNode = {
    type,
    props: {},
    children: [],
    parent: null,
    setAttribute(name, value) {
      this.props[name] = value
    },
    focus() {
      focusedNode = node
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
  return new Function('VueRuntime', `${code}\nreturn render`)({ ...VueRuntime, vModelSelect: {}, vModelText: {}, vModelCheckbox: {} }) as (ctx: unknown, cache: unknown) => unknown
}

;(RepoDetailView as unknown as { render: unknown }).render = compileClientRender(repoSource, 'RepoDetailView.vue')
;(ConnectionDetailView as unknown as { render: unknown }).render = compileClientRender(connectionSource, 'ConnectionDetailView.vue')
;(DashboardTile as unknown as { render: unknown }).render = compileClientRender(dashboardSource, 'DashboardTile.vue')

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

function mount(component: typeof RepoDetailView | typeof ConnectionDetailView | typeof DashboardTile, props: Record<string, unknown>) {
  const root = createTreeElement('root')
  const app = renderer.createApp(component, props)
  app.provide(ssrContextKey, { modules: new Set<string>() })
  const proxy = app.mount(root) as unknown as { $: { props: Record<string, unknown>; setupState: Record<string, any> } }
  return { app, root, instance: proxy.$ }
}

const repository: RepositoryDetail = {
  name: 'orders',
  uid: 'orders-uid',
  connectionRef: 'github',
  repo: 'orders',
  owner: 'railgrid',
  visibility: 'private',
  ready: true,
  conditions: [],
}

const createdCollaborator: Collaborator = {
  name: 'orders-alice',
  repositoryRef: repository.name,
  username: 'alice',
  permission: 'push',
  invitationPending: true,
  ready: false,
}

const connection: ConnectionDetail = {
  name: 'github',
  uid: 'github-uid',
  provider: 'github',
  type: 'pat',
  owner: 'railgrid',
  secretName: 'github-token',
  scopes: ['repo'],
  validated: true,
  conditions: [],
}

const noPackages: Package[] = []

async function mountCollaboratorAccess() {
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.listDeployKeys.mockResolvedValue([])
  mocks.api.listCollaborators.mockResolvedValue([])
  mocks.api.listPackagesPage.mockResolvedValue({ items: [], continue: '', resourceVersion: '' })
  const mounted = mount(RepoDetailView, { name: repository.name, deletions: createResourceDeletions() })
  await settle()
  click(findNode(mounted.root, node => node.type === 'button' && textContent(node).includes('Manage access'))!)
  await settle()
  return mounted
}

let previousWindow: unknown

beforeEach(() => {
  focusedNode = null
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
  mocks.api.createCollaborator.mockResolvedValue(createdCollaborator)
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

describe('repository collaborator validation', () => {
  it.each(['', '   ', '\t\n'])('blocks an empty or whitespace-only username (%j) with a focused, associated field error', async username => {
    const { app, root, instance } = await mountCollaboratorAccess()
    try {
      instance.setupState.collabUser = username
      focusedNode = null
      await instance.setupState.addCollab()
      await settle()

      const input = findNode(root, node => node.type === 'input' && node.props.id === 'code-repository-collaborator-username')!
      const error = findNode(root, node => node.props.id === 'code-repository-collaborator-username-error')!
      expect(mocks.api.createCollaborator).not.toHaveBeenCalled()
      expect(instance.setupState.collabUser).toBe(username)
      expect(input.props['aria-invalid']).toBe('true')
      expect(input.props['aria-describedby']).toBe('code-repository-collaborator-username-error')
      expect(error.props.role).toBe('alert')
      expect(textContent(error)).toBe('Enter a GitHub username.')
      expect(focusedNode).toBe(input)
    } finally { app.unmount() }
  })

  it('keeps operation errors through correction and pending retry, then clears them on success', async () => {
    mocks.api.createCollaborator.mockRejectedValueOnce({ reason: 'HTTPError', message: 'first write failed' })
    const pending = deferred<Collaborator>()
    const { app, root, instance } = await mountCollaboratorAccess()
    try {
      instance.setupState.collabUser = 'alice'
      await instance.setupState.addCollab()
      await settle()

      const previousError = 'HTTPError: first write failed'
      expect(instance.setupState.collabError).toBe(previousError)
      instance.setupState.collabUser = '   '
      await instance.setupState.addCollab()
      await settle()
      expect(instance.setupState.collabFieldError).toBe('Enter a GitHub username.')
      expect(instance.setupState.collabError).toBe(previousError)

      instance.setupState.collabUser = 'alice'
      const input = findNode(root, node => node.type === 'input' && node.props.id === 'code-repository-collaborator-username')!
      const inputHandlers = Array.isArray(input.props.onInput) ? input.props.onInput : [input.props.onInput]
      for (const handler of inputHandlers) {
        if (typeof handler === 'function') handler({ target: { value: 'alice' } })
      }
      await settle()
      expect(instance.setupState.collabFieldError).toBeNull()
      expect(input.props['aria-invalid']).toBeUndefined()
      expect(input.props['aria-describedby']).toBeUndefined()
      expect(findNode(root, node => node.props.id === 'code-repository-collaborator-username-error')).toBeUndefined()
      expect(instance.setupState.collabError).toBe(previousError)

      mocks.api.createCollaborator.mockReturnValueOnce(pending.promise)
      const retry = instance.setupState.addCollab() as Promise<void>
      await settle()
      expect(instance.setupState.collabSubmitting).toBe(true)
      expect(instance.setupState.collabError).toBe(previousError)
      pending.resolve(createdCollaborator)
      await retry
      await settle()
      expect(instance.setupState.collabSubmitting).toBe(false)
      expect(instance.setupState.collabError).toBeNull()
    } finally { app.unmount() }
  })

  it('trims the username and sends the selected permission', async () => {
    const { app, instance } = await mountCollaboratorAccess()
    try {
      instance.setupState.collabUser = '  alice  '
      instance.setupState.collabPerm = 'admin'
      await instance.setupState.addCollab()

      expect(mocks.api.createCollaborator).toHaveBeenCalledWith({
        repositoryRef: repository.name,
        username: 'alice',
        permission: 'admin',
      })
      expect(instance.setupState.collabUser).toBe('')
    } finally { app.unmount() }
  })

  it('ignores a duplicate submit while a collaborator request is pending', async () => {
    const pending = deferred<Collaborator>()
    mocks.api.createCollaborator.mockReturnValueOnce(pending.promise)
    const { app, instance } = await mountCollaboratorAccess()
    try {
      instance.setupState.collabUser = 'alice'
      const firstSubmit = instance.setupState.addCollab() as Promise<void>
      await settle()
      await instance.setupState.addCollab()

      expect(mocks.api.createCollaborator).toHaveBeenCalledTimes(1)
      expect(instance.setupState.collabSubmitting).toBe(true)
      pending.resolve(createdCollaborator)
      await firstSubmit
      await settle()
      expect(instance.setupState.collabSubmitting).toBe(false)
    } finally { app.unmount() }
  })

  it('keeps the username draft when collaborator creation fails', async () => {
    mocks.api.createCollaborator.mockRejectedValueOnce({ reason: 'HTTPError', message: 'write unavailable' })
    const { app, instance } = await mountCollaboratorAccess()
    try {
      instance.setupState.collabUser = '  alice  '
      await instance.setupState.addCollab()

      expect(instance.setupState.collabUser).toBe('  alice  ')
      expect(instance.setupState.collabError).toBe('HTTPError: write unavailable')
    } finally { app.unmount() }
  })
})

describe('mounted resource deletion state', () => {
  it.each([
    { component: RepoDetailView, label: 'Refresh repository', read: mocks.api.getRepository, data: repository },
    { component: ConnectionDetailView, label: 'Refresh connection', read: mocks.api.getConnection, data: connection },
  ])('keeps $label focusable and ignores duplicate requests', async ({ component, label, read, data }) => {
    mocks.api.listConnections.mockResolvedValue([])
    mocks.api.listDeployKeys.mockResolvedValue([])
    mocks.api.listCollaborators.mockResolvedValue([])
    mocks.api.listPackagesPage.mockResolvedValue({ items: [], continue: '', resourceVersion: '' })
    const { app, root } = mount(component, { name: data.name, deletions: createResourceDeletions() })
    try {
      await settle()
      const pending = deferred<typeof data>()
      read.mockReturnValueOnce(pending.promise)
      const before = read.mock.calls.length
      const button = findNode(root, node => node.type === 'button' && node.props['aria-label'] === label)!
      click(button); click(button)
      await settle()
      expect(read.mock.calls.length).toBe(before + 1)
      expect(button.props.disabled).toBe(false)
      expect(button.props['aria-disabled']).toBe(true)
      expect(button.props['aria-busy']).toBe(true)
      expect(findNode(button, node => node.type === 'svg' && String(node.props.class).includes('k-spin'))).toBeDefined()
      pending.resolve(data)
      await settle()
      expect(button.props['aria-disabled']).toBeUndefined()
      expect(findNode(button, node => node.type === 'svg' && String(node.props.class).includes('k-spin'))).toBeUndefined()
    } finally { app.unmount() }
  })

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

    // What api.ts surfaces for a non-Status kcp failure (a KubeError mapped to
    // the portal's {reason, message} contract).
    deleteRequest.reject({ reason: 'HTTPError', message: '500: delete failed' })
    await settle()

    expect(textContent(root)).not.toContain('Deleting this repository.')
    expect(textContent(root)).toContain('HTTPError: 500: delete failed')
    expect(textContent(root)).toContain('Could not delete repository orders')
    mocks.confirmDialog.mockResolvedValueOnce(false)
    click(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More repository actions')!)
    await settle()
    click(findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Delete repository')!)
    await settle()
    expect(textContent(root)).toContain('HTTPError: 500: delete failed')
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

    // What api.ts surfaces for a non-Status kcp failure (a KubeError mapped to
    // the portal's {reason, message} contract).
    deleteRequest.reject({ reason: 'HTTPError', message: '500: delete failed' })
    await settle()

    expect(textContent(root)).not.toContain('Deleting this connection.')
    expect(textContent(root)).toContain('HTTPError: 500: delete failed')
    expect(textContent(root)).toContain('github')
    expect(back).not.toHaveBeenCalled()
    expect(mocks.toast).not.toHaveBeenCalled()
    expect(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')?.props.disabled).toBe(false)
    app.unmount()
  })

  it('emits one informational toast before leaving a successfully deleted connection', async () => {
    const back = vi.fn()
    const { app, root } = mount(ConnectionDetailView, {
      name: connection.name,
      deletions: createResourceDeletions(),
      onBack: back,
    })
    await settle()

    click(findNode(root, node => node.type === 'button' && node.props['aria-label'] === 'More connection actions')!)
    await settle()
    click(findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Delete connection')!)
    await settle()

    expect(mocks.toast).toHaveBeenCalledTimes(1)
    expect(mocks.toast).toHaveBeenCalledWith('info', 'Connection deletion requested for github.')
    expect(back).toHaveBeenCalledTimes(1)
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
    expect(mocks.toast).not.toHaveBeenCalled()
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

describe('Code dashboard read state', () => {
  it('retains the snapshot when a deprecated token changes under the same host fetch', async () => {
    mocks.api.listRepositories.mockResolvedValue([repository])
    mocks.api.listConnections.mockResolvedValue([connection])
    const hostFetch = vi.fn()
    const { app, instance } = mount(DashboardTile, { context: { tenant: 'tenant-a', fetch: hostFetch, token: 'old' } })
    try {
      await settle()
      expect(instance.setupState.repositories).toEqual([repository])
      const reads = mocks.api.listRepositories.mock.calls.length
      instance.props.context = { tenant: 'tenant-a', fetch: hostFetch, token: 'new' }
      await settle()
      expect(instance.setupState.loaded).toBe(true)
      expect(instance.setupState.repositories).toEqual([repository])
      expect(mocks.api.listRepositories).toHaveBeenCalledTimes(reads)
    } finally { app.unmount() }
  })

  it('clears the snapshot when the host fetch changes', async () => {
    mocks.api.listRepositories.mockResolvedValue([repository])
    mocks.api.listConnections.mockResolvedValue([connection])
    const { app, root, instance } = mount(DashboardTile, { context: { tenant: 'tenant-a', fetch: vi.fn() } })
    try {
      await settle()
      expect(instance.setupState.repositories).toEqual([repository])
      const replacement = deferred<typeof repository[]>()
      mocks.api.listRepositories.mockReturnValueOnce(replacement.promise)
      instance.props.context = { tenant: 'tenant-a', fetch: vi.fn() }
      await settle()
      expect(instance.setupState.loaded).toBe(false)
      expect(instance.setupState.repositories).toEqual([])
      expect(textContent(root)).toContain('Loading repositories')
      replacement.resolve([])
      await settle()
      expect(instance.setupState.loaded).toBe(true)
      expect(instance.setupState.repositories).toEqual([])
    } finally { app.unmount() }
  })

  it('clears the repository on credential replacement and fences a caller change', async () => {
    mocks.api.listRepositories.mockResolvedValue([repository])
    mocks.api.listConnections.mockResolvedValue([connection])
    const { app, root, instance } = mount(DashboardTile, { context: { tenant: 'tenant-a', token: 'old-token', user: { userId: 'user-a' } } })
    try {
      await settle()
      const state = instance.setupState
      expect(state.loaded).toBe(true)
      expect(findNode(root, node => node.type === 'button' && String(node.props['aria-label']).includes('Open repository'))).toBeDefined()

      const renewal = deferred<typeof repository[]>()
      mocks.api.listRepositories.mockReturnValueOnce(renewal.promise)
      instance.props.context = { tenant: 'tenant-a', token: 'new-token', user: { userId: 'user-a' } }
      await settle()
      expect(state.loaded).toBe(false)
      expect(state.repositories).toEqual([])
      expect(findNode(root, node => node.type === 'button' && String(node.props['aria-label']).includes('Open repository'))).toBeUndefined()

      mocks.api.listRepositories.mockResolvedValueOnce([{ ...repository, name: 'caller-b-repository' }])
      instance.props.context = { tenant: 'tenant-a', token: 'caller-b-token', user: { userId: 'user-b' } }
      await settle()
      expect(state.loaded).toBe(false)
      expect(state.repositories).toEqual([])
      renewal.resolve([{ ...repository, name: 'stale-caller-a-repository' }])
      await settle()
      await settle()
      expect(state.repositories).toEqual([{ ...repository, name: 'caller-b-repository' }])
    } finally { app.unmount() }
  })
})

it('keeps cached connection choices while a failed read is retried once', async () => {
  const retry = deferred<Connection[]>()
  mocks.api.listConnections.mockReset().mockResolvedValueOnce([connection]).mockRejectedValueOnce({ reason: 'HTTPError', message: 'connections unavailable' }).mockReturnValueOnce(retry.promise)
  mocks.api.listDeployKeys.mockResolvedValue([])
  mocks.api.listCollaborators.mockResolvedValue([])
  mocks.api.listPackagesPage.mockResolvedValue({ items: [], continue: '', resourceVersion: '' })
  const { app, root, instance } = mount(RepoDetailView, { name: repository.name, deletions: createResourceDeletions() })
  try {
    await settle()
    instance.setupState.connectionExpanded = true
    instance.setupState.loadConnections()
    await settle()
    expect(textContent(root)).toContain('Showing cached connection choices')
    const retryButton = findNode(root, node => node.type === 'button' && textContent(node).trim() === 'Retry')!
    click(retryButton)
    click(retryButton)
    await settle()
    expect(mocks.api.listConnections).toHaveBeenCalledTimes(3)
    expect(retryButton.props['aria-busy']).toBe('true')
    expect(textContent(root)).toContain('Showing cached connection choices')
    retry.resolve([connection])
    await settle()
    expect(textContent(root)).not.toContain('connections unavailable')
  } finally { app.unmount() }
})

it('retains a failed key removal when a different key succeeds or confirmation is cancelled', async () => {
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.listDeployKeys.mockResolvedValue([])
  mocks.api.listCollaborators.mockResolvedValue([])
  mocks.api.listPackagesPage.mockResolvedValue({ items: [], continue: '', resourceVersion: '' })
  mocks.api.deleteDeployKey.mockReset().mockRejectedValueOnce({ message: 'key A failed' }).mockResolvedValueOnce(undefined)
  const { app, instance } = mount(RepoDetailView, { name: repository.name, deletions: createResourceDeletions() })
  const first = { name: 'key-a', uid: 'a', title: 'Key A', repositoryRef: repository.name }
  const second = { name: 'key-b', uid: 'b', title: 'Key B', repositoryRef: repository.name }
  try {
    await settle()
    await instance.setupState.removeKey(first)
    expect(instance.setupState.keyDeleteErrors['key-a:a']).toContain('Key A: key A failed')
    mocks.confirmDialog.mockResolvedValueOnce(false)
    await instance.setupState.removeKey(first)
    expect(instance.setupState.keyDeleteErrors['key-a:a']).toContain('key A failed')
    await instance.setupState.removeKey(second)
    expect(instance.setupState.keyDeleteErrors['key-a:a']).toContain('key A failed')
    expect(mocks.api.deleteDeployKey).toHaveBeenCalledTimes(2)
  } finally { app.unmount() }
})

it('retains a failed collaborator removal until the matching identity succeeds', async () => {
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.listDeployKeys.mockResolvedValue([])
  mocks.api.listCollaborators.mockResolvedValue([])
  mocks.api.listPackagesPage.mockResolvedValue({ items: [], continue: '', resourceVersion: '' })
  mocks.api.deleteCollaborator.mockReset().mockRejectedValueOnce({ message: 'remove failed' }).mockResolvedValue(undefined)
  const { app, instance } = mount(RepoDetailView, { name: repository.name, deletions: createResourceDeletions() })
  const first = { name: 'member', uid: 'old', username: 'alice', repositoryRef: repository.name }
  try {
    await settle()
    await instance.setupState.removeCollab(first)
    expect(instance.setupState.collabDeleteErrors['member:old']).toContain('alice: remove failed')
    await instance.setupState.removeCollab({ ...first, uid: 'new' })
    expect(instance.setupState.collabDeleteErrors['member:old']).toContain('remove failed')
    await instance.setupState.removeCollab(first)
    expect(instance.setupState.collabDeleteErrors['member:old']).toBeUndefined()
  } finally { app.unmount() }
})

it.each([
  { component: RepoDetailView, remove: 'deleteRepository', action: 'deleteRepository', data: repository, field: 'repositoryMutationError' },
  { component: ConnectionDetailView, remove: 'deleteConnection', action: 'deleteConnection', data: connection, field: 'mutationError' },
] as const)('ignores a pending $remove result invalidated by context rotation', async ({ component, remove, action, data, field }) => {
  const pending = deferred<void>()
  mocks.api[remove].mockReset().mockReturnValue(pending.promise)
  const { app, instance } = mount(component, { name: data.name, deletions: createResourceDeletions() })
  try {
    await settle()
    instance.setupState[field] = 'earlier failure'
    const actionPromise = instance.setupState[action]() as Promise<void>
    await settle()
    pending.reject({ reason: 'ContextChanged', message: 'old context' })
    await actionPromise
    await settle()
    expect(instance.setupState[field]).toBe('earlier failure')
    expect(mocks.toast).not.toHaveBeenCalled()
  } finally { app.unmount() }
})

it.each([
  { action: 'removeKey', remove: 'deleteDeployKey', field: 'keyDeleteErrors', row: { name: 'key', uid: 'k', title: 'Key', repositoryRef: repository.name } },
  { action: 'removeCollab', remove: 'deleteCollaborator', field: 'collabDeleteErrors', row: { name: 'member', uid: 'm', username: 'alice', repositoryRef: repository.name } },
] as const)('ignores a pending $remove result invalidated by context rotation', async ({ action, remove, field, row }) => {
  const pending = deferred<void>()
  mocks.api[remove].mockReset().mockReturnValue(pending.promise)
  const { app, instance } = mount(RepoDetailView, { name: repository.name, deletions: createResourceDeletions() })
  try {
    await settle()
    instance.setupState[field] = { earlier: 'previous failure' }
    const actionPromise = instance.setupState[action](row) as Promise<void>
    await settle()
    pending.reject({ reason: 'ContextChanged', message: 'old context' })
    await actionPromise
    expect(instance.setupState[field]).toEqual({ earlier: 'previous failure' })
    expect(mocks.toast).not.toHaveBeenCalled()
  } finally { app.unmount() }
})
