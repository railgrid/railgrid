import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

const css = readFileSync(new URL('./agent-ui.css', import.meta.url), 'utf8')
const activityCSS = readFileSync(new URL('./activity.css', import.meta.url), 'utf8')
const conversationCSS = readFileSync(new URL('./conversation.css', import.meta.url), 'utf8')
const source = readFileSync(new URL('./styles.ts', import.meta.url), 'utf8')
const coreCSS = readFileSync(new URL('../portalkit/railgrid-ui.css', import.meta.url), 'utf8')
const coreSource = readFileSync(new URL('../portalkit/styles.ts', import.meta.url), 'utf8')

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

function environment({ coreVersion = '', agentVersion = '', existingNodes = [] } = {}) {
  const computedValues = new Map([
    ['--railgrid-ui-canonical', '1'],
    ['--railgrid-ui-core-version', coreVersion],
    ['--railgrid-agent-ui-canonical', agentVersion ? '1' : ''],
    ['--railgrid-agent-ui-version', agentVersion],
  ])
  const nodes = new Map(existingNodes.map(node => [node.id, node]))
  const document = {
    documentElement: { style: { getPropertyValue: name => computedValues.get(name) ?? '' } },
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
        if (node.getAttribute('data-railgrid-ui-core-version')) {
          computedValues.set('--railgrid-ui-core-version', node.getAttribute('data-railgrid-ui-core-version'))
        }
        if (node.getAttribute('data-railgrid-agent-ui-version')) {
          computedValues.set('--railgrid-agent-ui-canonical', '1')
          computedValues.set('--railgrid-agent-ui-version', node.getAttribute('data-railgrid-agent-ui-version'))
        }
        return node
      },
    },
  }
  return { computedValues, document }
}

function loadAgentHelper(options = {}) {
  const env = environment(options)
  const context = {
    document: env.document,
    window: { getComputedStyle: () => ({ getPropertyValue: name => env.computedValues.get(name) ?? '' }) },
    __coreCalls: 0,
    __ensureCore: options.ensureCore ?? (() => {}),
  }
  const executable = source
    .replace(/^import agentUIStyles.*$/m, "const agentUIStyles = 'agent-css';")
    .replace(/^import activityStyles.*$/m, "const activityStyles = 'activity-css';")
    .replace(/^import conversationStyles.*$/m, "const conversationStyles = 'conversation-css';")
    .replace(/^import \{ ensureRailgridUIStyles \}.*$/m, 'const ensureRailgridUIStyles = () => { globalThis.__coreCalls += 1; globalThis.__ensureCore(); };')
    .replaceAll('export const ', 'const ')
    .replaceAll('export function ', 'function ')
    .replaceAll(': string', '')
    .replaceAll(': boolean', '')
    .replaceAll(': void', '')
    + '\n;globalThis.__styles = { ensureAgentUIStyles, AGENT_UI_STYLE_ID, AGENT_UI_VERSION };'
  runInNewContext(executable, context)
  return { ...context.__styles, document: env.document, computedValues: env.computedValues, context }
}

test('AgentKit owns optional recipes and keeps the core contract separate', () => {
  assert.match(css, /--railgrid-agent-ui-canonical:\s*1;/)
  assert.match(css, /--railgrid-agent-ui-version:\s*6;/)
  assert.match(source, /conversation\.css\?inline/)
  assert.match(css, /\.k-ai-conversation-layout\s*\{/)
  assert.match(css, /\.k-workbench-tabs\s*\{/)
  assert.match(css, /\.k-workbench-tabs__strip\s*\{/)
  assert.match(css, /\.k-ai-launcher\s*\{/)
  assert.match(css, /\.k-model-connection\s*[,\{]/)
  assert.match(conversationCSS, /\.k-ai-turn-progress\s*\{/)

  assert.match(coreCSS, /--railgrid-ui-core-version:\s*25;/)
  assert.doesNotMatch(coreCSS, /--railgrid-ui-version/)
  assert.match(coreCSS, /\.k-back-action--icon-only\s*\{/)
  assert.doesNotMatch(coreCSS, /\.k-ai-|\.k-workbench-|\.k-model-/)
  assert.doesNotMatch(coreCSS, /--railgrid-agent-ui-/)
  assert.match(coreSource, /RAILGRID_UI_CORE_VERSION_MARKER/)
})

test('AgentKit is opt-in, depends on core styles, and is idempotent', () => {
  const helper = loadAgentHelper({ coreVersion: '18' })
  assert.equal(helper.document.head.children.length, 0, 'loading the module must not inject styles')

  helper.ensureAgentUIStyles()
  assert.equal(helper.context.__coreCalls, 1)
  assert.deepEqual(helper.document.head.children.map(node => node.id), ['k-agent-ui'])
  assert.equal(helper.document.head.children[0].textContent, 'agent-css\nactivity-css\nconversation-css')
  assert.equal(helper.document.head.children[0].getAttribute('data-railgrid-agent-ui-version'), '6')

  helper.ensureAgentUIStyles()
  assert.equal(helper.document.head.children.length, 1)
})

test('AgentKit preserves stale style nodes and accepts current or newer hosts', () => {
  const staleNode = styleNode('k-agent-ui', 'stale-agent-css')
  const stale = loadAgentHelper({ coreVersion: '18', agentVersion: '4', existingNodes: [staleNode] })
  stale.ensureAgentUIStyles()
  assert.deepEqual(stale.document.head.children.map(node => node.id), ['k-agent-ui-v6'])
  assert.equal(staleNode.textContent, 'stale-agent-css')

  const current = loadAgentHelper({ coreVersion: '18', agentVersion: '6' })
  current.ensureAgentUIStyles()
  assert.equal(current.document.head.children.length, 0)

  const newer = loadAgentHelper({ coreVersion: '18', agentVersion: '7' })
  newer.ensureAgentUIStyles()
  assert.equal(newer.document.head.children.length, 0)
})
