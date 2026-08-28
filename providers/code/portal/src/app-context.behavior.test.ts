import { defineComponent, h, inject, nextTick, reactive } from 'vue'
import { createRenderer, ssrContextKey, type RendererOptions } from '@vue/runtime-core'
import * as VueRuntime from 'vue'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { describe, expect, it, vi } from 'vitest'

import { contextGenerationKey } from './context'
import type { FarosContext } from './types'

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
vi.mock('./views/ConnectionCreateView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/ConnectionDetailView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/RepositoriesView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/RepositoryCreateView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/RepoDetailView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/PackagesView.vue', () => ({ default: { setup: () => () => null } }))
vi.mock('./views/ConnectionsView.vue', () => ({
  default: defineComponent({
    setup() {
      captured.generation = inject(contextGenerationKey)
      return () => h('div')
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
}

function node(type: string): TreeNode {
  return { type, children: [], parent: null, props: {} }
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

describe('Code App context authority generation', () => {
  it('increments synchronously for every shell authority field before child unmount', async () => {
    captured.generation = undefined
    const context = reactive<FarosContext>({
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
})
