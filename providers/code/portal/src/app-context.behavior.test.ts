import { defineComponent, h, inject, nextTick, reactive, ref } from 'vue'
import { createRenderer, ssrContextKey, type RendererOptions } from '@vue/runtime-core'
import * as VueRuntime from 'vue'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { describe, expect, it, vi } from 'vitest'

import { contextGenerationKey } from './context'
import type { RailgridContext } from './types'

const captured = vi.hoisted(() => ({ generation: undefined as Readonly<{ value: number }> | undefined }))

vi.mock('./api', () => ({
  setAPIContext: vi.fn(),
  setBasePath: vi.fn(),
}))
vi.mock('./portalkit/confirm', () => ({ resolveConfirm: vi.fn() }))
vi.mock('./portalkit/Tabs.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./portalkit/ConfirmDialog.vue', () => ({
  default: defineComponent({
    setup() {
      captured.generation = inject(contextGenerationKey)
      return () => null
    },
  }),
}))
vi.mock('./views/ConnectionCreateView.vue', () => ({
  default: defineComponent({
    emits: ['created', 'cancel', 'use-token'],
    setup(_props, { emit }) {
      return () => h('div', [
        h('button', { id: 'complete-connection', onClick: () => emit('created', 'github-created') }),
        h('button', { id: 'cancel-connection', onClick: () => emit('cancel') }),
        h('button', { id: 'use-token', onClick: () => emit('use-token') }),
      ])
    },
  }),
}))
vi.mock('./views/ConnectionDetailView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/RepositoriesView.vue', () => ({
  default: defineComponent({
    emits: ['create-connection'],
    setup(_props, { emit }) {
      return () => h('button', { id: 'start-repository-connection', onClick: () => emit('create-connection') })
    },
  }),
}))
vi.mock('./views/RepositoryCreateView.vue', () => ({
  default: defineComponent({
    props: ['initialDraft'],
    emits: ['create-connection'],
    setup(props, { emit }) {
      return () => h('div', [
        h('span', { id: 'restored-draft', value: props.initialDraft?.name || '', connection: props.initialDraft?.connectionRef || '' }),
        h('button', { id: 'draft-connection', onClick: () => emit('create-connection', { name: 'draft-name', repo: 'owner/repo', connectionRef: 'old', visibility: 'private', description: 'draft', autoInit: true }) }),
      ])
    },
  }),
}))
vi.mock('./views/RepoDetailView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/PackagesView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/ConnectionsView.vue', () => ({
  default: defineComponent({
    emits: ['create', 'cancel-prerequisite'],
    setup(_props, { emit }) {
      captured.generation = inject(contextGenerationKey)
      return () => h('div', [
        h('button', { id: 'choose-token', onClick: () => emit('create', 'token') }),
        h('button', { id: 'choose-github', onClick: () => emit('create', 'github') }),
        h('button', { id: 'return-repository', onClick: () => emit('cancel-prerequisite') }),
      ])
    },
  }),
}))
vi.mock('lucide-vue-next', () => ({ GitBranch: {}, Package: {}, Plug: {} }))

import App from './App.vue'
import appSource from './App.vue?raw'

function compileClientRender(source: string): (ctx: unknown, cache: unknown) => unknown {
  const parsed = parse(source, { filename: 'App.vue' })
  if (!parsed.descriptor.template) throw new Error('App.vue has no template')
  const script = compileScript(parsed.descriptor, { id: 'behavior-App.vue' })
  const compiled = compileTemplate({
    source: parsed.descriptor.template.content,
    filename: 'App.vue',
    id: 'behavior-App.vue',
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

;(App as unknown as { render: unknown }).render = compileClientRender(appSource)

interface TreeNode {
  type: string
  children: TreeNode[]
  parent: TreeNode | null
  props: Record<string, unknown>
  text?: string
  dispatchEvent(event: Event): boolean
}

const navigationEvents: CustomEvent[] = []

function node(type: string): TreeNode {
  return {
    type,
    children: [],
    parent: null,
    props: {},
    dispatchEvent(event) {
      if (event instanceof CustomEvent && event.type === 'railgrid-navigate') navigationEvents.push(event)
      return true
    },
  }
}

const rendererOptions: RendererOptions<TreeNode, TreeNode> = {
  patchProp(target, key, _previous, value) {
    target.props[key] = value
  },
  insert(child, parent, anchor) {
    child.parent = parent
    if (!anchor) {
      parent.children.push(child)
      return
    }
    const index = parent.children.indexOf(anchor)
    parent.children.splice(index < 0 ? parent.children.length : index, 0, child)
  },
  remove(child) {
    if (!child.parent) return
    const index = child.parent.children.indexOf(child)
    if (index >= 0) child.parent.children.splice(index, 1)
    child.parent = null
  },
  createElement: type => node(type),
  createText: text => ({ ...node('#text'), text }),
  createComment: text => ({ ...node('#comment'), text }),
  setText(target, text) {
    target.text = text
  },
  setElementText(target, text) {
    target.children = [{ ...node('#text'), parent: target, text }]
  },
  parentNode: target => target.parent,
  nextSibling: target => {
    if (!target.parent) return null
    const index = target.parent.children.indexOf(target)
    return index >= 0 ? target.parent.children[index + 1] || null : null
  },
}

const renderer = createRenderer(rendererOptions)

function allNodes(root: TreeNode): TreeNode[] {
  return [root, ...root.children.flatMap(allNodes)]
}

function click(root: TreeNode, id: string): void {
  const target = allNodes(root).find(item => item.props.id === id)
  if (!target || typeof target.props.onClick !== 'function') throw new Error(`button ${id} was not rendered`)
  target.props.onClick()
}

describe('Code App context authority generation', () => {
  it.each(['standalone', 'prerequisite'])('switches to token setup and preserves %s journey', async (journey) => {
    const path = journey === 'prerequisite' ? 'import/repository' : 'create/connection/github'
    const context = reactive<RailgridContext>({ basePath: '/ui/providers/code', tenant: 'tenant-a', user: { sub: 'user-a' }, subPath: path })
    const root = node('root')
    const app = renderer.createApp(App, { ctx: context })
    app.provide(ssrContextKey, { modules: new Set<string>() })
    app.mount(root)
    if (journey === 'prerequisite') {
      click(root, 'draft-connection')
      context.subPath = 'connections'
      await nextTick()
      click(root, 'choose-github')
      context.subPath = 'create/connection/github'
      await nextTick()
    }
    click(root, 'use-token')
    expect(navigationEvents.at(-1)?.detail).toEqual({ path: 'create/connection/token' })
    context.subPath = 'create/connection/token'
    await nextTick()
    click(root, 'complete-connection')
    expect(navigationEvents.at(-1)?.detail).toEqual({ path: journey === 'prerequisite' ? path : 'connections/github-created', replace: true })
    if (journey === 'prerequisite') {
      context.subPath = path
      await nextTick()
      expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.value).toBe('draft-name')
      expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.connection).toBe('github-created')
    }
    app.unmount()
  })
  it.each([['import/repository', 'token'], ['create/repository', 'token'], ['import/repository', 'github'], ['create/repository', 'github']])('restores an in-memory %s draft after %s connection succeeds', async (path, method) => {
    const context = reactive<RailgridContext>({ basePath: '/ui/providers/code', tenant: 'tenant-a', user: { sub: 'user-a' }, subPath: path })
    const root = node('root')
    const app = renderer.createApp(App, { ctx: context })
    app.provide(ssrContextKey, { modules: new Set<string>() })
    app.mount(root)
    click(root, 'draft-connection')
    context.subPath = 'connections'
    await nextTick()
    click(root, `choose-${method}`)
    expect(navigationEvents.at(-1)?.detail).toEqual({ path: `create/connection/${method}` })
    context.subPath = `create/connection/${method}`
    await nextTick()
    click(root, 'complete-connection')
    context.subPath = path
    await nextTick()
    expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.value).toBe('draft-name')
    expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.connection).toBe('github-created')
    context.user = { sub: 'user-b' }
    await nextTick()
    expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.value).toBe('')
    app.unmount()
  })
  it.each(['cancel', 'deviation'])('discards a repository draft after %s', async (outcome) => {
    const context = reactive<RailgridContext>({ basePath: '/ui/providers/code', tenant: 'tenant-a', user: { sub: 'user-a' }, subPath: 'create/repository' })
    const root = node('root')
    const app = renderer.createApp(App, { ctx: context })
    app.provide(ssrContextKey, { modules: new Set<string>() })
    app.mount(root)
    click(root, 'draft-connection')
    context.subPath = 'connections'
    await nextTick()
    click(root, 'choose-token')
    context.subPath = 'create/connection/token'
    await nextTick()
    if (outcome === 'cancel') click(root, 'cancel-connection')
    context.subPath = 'repositories'
    await nextTick()
    context.subPath = 'create/repository'
    await nextTick()
    expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.value).toBe('')
    app.unmount()
  })
  it('returns to the draft from the connection choice when a connection appears', async () => {
    const context = reactive<RailgridContext>({ basePath: '/ui/providers/code', tenant: 'tenant-a', user: { sub: 'user-a' }, subPath: 'import/repository' })
    const root = node('root')
    const app = renderer.createApp(App, { ctx: context })
    app.provide(ssrContextKey, { modules: new Set<string>() })
    app.mount(root)
    click(root, 'draft-connection')
    context.subPath = 'connections'
    await nextTick()
    click(root, 'return-repository')
    expect(navigationEvents.at(-1)?.detail).toEqual({ path: 'import/repository', replace: true })
    context.subPath = 'import/repository'
    await nextTick()
    expect(allNodes(root).find(item => item.props.id === 'restored-draft')?.props.value).toBe('draft-name')
    app.unmount()
  })

  it.each(['connections', 'create/connection/github'])('retains only the return route across reload on %s', async (path) => {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
    const values = new Map<string, string>()
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { sessionStorage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
      removeItem: (key: string) => { values.delete(key) },
    } } })
    try {
      const context = reactive<RailgridContext>({ basePath: '/ui/providers/code', tenant: 'tenant-a', user: { sub: 'user-a' }, subPath: 'import/repository' })
      const mountApp = () => { const root = node('root'); const app = renderer.createApp(App, { ctx: context }); app.provide(ssrContextKey, { modules: new Set<string>() }); app.mount(root); return { root, app } }
      let mounted = mountApp()
      click(mounted.root, 'draft-connection')
      context.subPath = 'connections'
      await nextTick()
      if (path !== 'connections') { click(mounted.root, 'choose-github'); context.subPath = path; await nextTick() }
      expect([...values.values()].join()).not.toContain('draft-name')
      mounted.app.unmount()
      mounted = mountApp()
      if (path === 'connections') { click(mounted.root, 'choose-github'); context.subPath = 'create/connection/github'; await nextTick() }
      click(mounted.root, 'complete-connection')
      expect(navigationEvents.at(-1)?.detail).toEqual({ path: 'import/repository', replace: true })
      mounted.app.unmount()
    } finally {
      if (descriptor) Object.defineProperty(globalThis, 'window', descriptor)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  })

  it('retains view identity on credential renewal while fencing mutations and clearing a caller change', async () => {
    const context = reactive<RailgridContext>({ basePath: '/ui/providers/code', tenant: 'same-tenant', token: 'token-a', user: { userId: 'user-a' }, subPath: 'connections' })
    const root = node('root')
    const app = renderer.createApp(App, { ctx: context })
    app.provide(ssrContextKey, { modules: new Set<string>() })
    const proxy = app.mount(root) as unknown as { $: { setupState: { viewGeneration: number; contextGeneration: number } } }
    const state = proxy.$.setupState
    const viewGeneration = state.viewGeneration
    const mutationGeneration = state.contextGeneration
    try {
      context.token = 'token-b'
      await nextTick()
      expect(state.viewGeneration).toBe(viewGeneration)
      expect(state.contextGeneration).toBeGreaterThan(mutationGeneration)
      context.user = { userId: 'user-b' }
      await nextTick()
      expect(state.viewGeneration).toBeGreaterThan(viewGeneration)
    } finally { app.unmount() }
  })
  it('preserves collection authority through route and theme context replacements', async () => {
    captured.generation = undefined
    const context = ref<RailgridContext>({
      basePath: '/ui/providers/code', token: 'same-token', tenant: 'same-tenant',
      user: { sub: 'same-user', email: 'same@example.com' }, subPath: 'connections', theme: 'light',
    })
    const host = defineComponent({ setup: () => () => h(App, { ctx: context.value }) })
    const root = node('root')
    const app = renderer.createApp(host)
    app.provide(ssrContextKey, { modules: new Set<string>() })
    app.mount(root)
    const generation = captured.generation!.value

    // The real host sends a fresh context object on each navigation and theme
    // change. Those replacements must not revoke cached collection state.
    for (const next of [
      { subPath: 'connections/github' },
      { subPath: 'connections' },
      { theme: 'dark' as const },
      { fetch: (() => Promise.reject(new Error('unused test transport'))) as RailgridContext['fetch'] },
    ]) {
      context.value = { ...context.value, ...next, user: { ...context.value.user } }
      await nextTick()
      expect(captured.generation!.value).toBe(generation)
    }

    context.value = { ...context.value, tenant: 'another-tenant' }
    await nextTick()
    expect(captured.generation!.value).toBeGreaterThan(generation)
    app.unmount()
  })

  it('increments synchronously for every shell authority field before child unmount', async () => {
    captured.generation = undefined
    const context = reactive<RailgridContext>({
      basePath: '/ui/providers/code',
      token: 'old-token',
      tenant: 'old-tenant',
      user: { sub: 'old-user', email: 'old@example.com' },
      subPath: '',
    })
    const host = defineComponent({ setup: () => () => h(App, { ctx: context }) })
    const root = node('root')
    const app = renderer.createApp(host)
    app.provide(ssrContextKey, { modules: new Set<string>() })
    app.mount(root)

    expect(captured.generation).toBeDefined()
    let expected = captured.generation!.value
    const changes: Array<() => void> = [
      () => { context.basePath = '/ui/providers/code-v2' },
      () => { context.token = 'new-token' },
      () => { context.tenant = 'new-tenant' },
      () => { context.user!.sub = 'new-user' },
      () => { context.user!.email = 'new@example.com' },
    ]
    for (const change of changes) {
      change()
      expected += 1
      expect(captured.generation!.value).toBe(expected)
    }

    await nextTick()
    app.unmount()
  })

  it('continues repository setup from in-memory intent when session storage is unavailable', async () => {
    navigationEvents.length = 0
    const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
    Reflect.deleteProperty(globalThis, 'window')
    try {
      const context = reactive<RailgridContext>({
        basePath: '/ui/providers/code',
        token: 'token',
        tenant: 'root:tenant-a',
        user: { sub: 'user-a' },
        subPath: 'repositories',
      })
      const root = node('root')
      const app = renderer.createApp(App, { ctx: context })
      app.provide(ssrContextKey, { modules: new Set<string>() })
      app.mount(root)
      click(root, 'start-repository-connection')
      expect(navigationEvents.at(-1)?.detail).toEqual({ path: 'connections' })

      context.subPath = 'connections'
      await nextTick()
      click(root, 'choose-token')
      context.subPath = 'create/connection/token'
      await nextTick()
      click(root, 'complete-connection')
      expect(navigationEvents.at(-1)?.detail).toEqual({ path: 'create/repository', replace: true })
      app.unmount()
    } finally {
      if (windowDescriptor) Object.defineProperty(globalThis, 'window', windowDescriptor)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  })

  it('revokes an in-memory repository continuation when the caller changes', async () => {
    navigationEvents.length = 0
    const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
    Reflect.deleteProperty(globalThis, 'window')
    try {
      const context = reactive<RailgridContext>({
        basePath: '/ui/providers/code',
        token: 'token-a',
        tenant: 'root:tenant-a',
        user: { userId: 'user-a' },
        subPath: 'repositories',
      })
      const root = node('root')
      const app = renderer.createApp(App, { ctx: context })
      app.provide(ssrContextKey, { modules: new Set<string>() })
      app.mount(root)
      click(root, 'start-repository-connection')

      context.subPath = 'connections'
      await nextTick()
      click(root, 'choose-token')
      context.subPath = 'create/connection/token'
      await nextTick()
      context.user = { userId: 'user-b' }
      context.token = 'token-b'
      await nextTick()
      click(root, 'complete-connection')
      expect(navigationEvents.at(-1)?.detail).toEqual({ path: 'connections/github-created', replace: true })
      app.unmount()
    } finally {
      if (windowDescriptor) Object.defineProperty(globalThis, 'window', windowDescriptor)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  })

  it('removes a persisted continuation when the caller changes before prerequisite navigation settles', async () => {
    navigationEvents.length = 0
    const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
    const values = new Map<string, string>()
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        sessionStorage: {
          getItem: (key: string) => values.get(key) ?? null,
          setItem: (key: string, value: string) => { values.set(key, value) },
          removeItem: (key: string) => { values.delete(key) },
        },
      },
    })
    try {
      const context = reactive<RailgridContext>({
        basePath: '/ui/providers/code',
        token: 'token-a',
        tenant: 'root:tenant-a',
        user: { sub: 'user-a' },
        subPath: 'repositories',
      })
      const root = node('root')
      const app = renderer.createApp(App, { ctx: context })
      app.provide(ssrContextKey, { modules: new Set<string>() })
      app.mount(root)
      click(root, 'start-repository-connection')
      expect(values.size).toBe(1)

      context.user = { sub: 'user-b' }
      context.token = 'token-b'
      await nextTick()
      expect(values.size).toBe(0)
      app.unmount()
    } finally {
      if (windowDescriptor) Object.defineProperty(globalThis, 'window', windowDescriptor)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  })
})
