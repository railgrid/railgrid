import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

const css = readFileSync(new URL('./agent-ui.css', import.meta.url), 'utf8')
const activityCSS = readFileSync(new URL('./activity.css', import.meta.url), 'utf8')
const conversationCSS = readFileSync(new URL('./conversation.css', import.meta.url), 'utf8')
const source = readFileSync(new URL('./styles.ts', import.meta.url), 'utf8')
const coreCSS = readFileSync(new URL('../portalkit/faros-ui.css', import.meta.url), 'utf8')
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
    ['--faros-ui-canonical', '1'],
    ['--faros-ui-core-version', coreVersion],
    ['--faros-agent-ui-canonical', agentVersion ? '1' : ''],
    ['--faros-agent-ui-version', agentVersion],
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
        if (node.getAttribute('data-faros-ui-core-version')) {
          computedValues.set('--faros-ui-core-version', node.getAttribute('data-faros-ui-core-version'))
        }
        if (node.getAttribute('data-faros-agent-ui-version')) {
          computedValues.set('--faros-agent-ui-canonical', '1')
          computedValues.set('--faros-agent-ui-version', node.getAttribute('data-faros-agent-ui-version'))
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
    .replace(/^import \{ ensureFarosUIStyles \}.*$/m, 'const ensureFarosUIStyles = () => { globalThis.__coreCalls += 1; globalThis.__ensureCore(); };')
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
  assert.match(css, /--faros-agent-ui-canonical:\s*1;/)
  assert.match(css, /--faros-agent-ui-version:\s*5;/)
  assert.match(source, /conversation\.css\?inline/)
  assert.match(css, /\.k-ai-conversation-layout\s*\{/)
  assert.match(css, /\.k-workbench-tabs\s*\{/)
  assert.match(css, /\.k-workbench-tabs__strip\s*\{/)
  assert.match(css, /\.k-ai-launcher\s*\{/)
  assert.match(css, /\.k-model-connection\s*[,\{]/)
  assert.match(conversationCSS, /\.k-ai-turn-progress\s*\{/)

  assert.match(coreCSS, /--faros-ui-core-version:\s*17;/)
  assert.doesNotMatch(coreCSS, /--faros-ui-version/)
  assert.match(coreCSS, /\.k-back-action--icon-only\s*\{/)
  assert.doesNotMatch(coreCSS, /\.k-ai-|\.k-workbench-|\.k-model-/)
  assert.doesNotMatch(coreCSS, /--faros-agent-ui-/)
  assert.match(coreSource, /FAROS_UI_CORE_VERSION_MARKER/)
})

test('AgentKit is opt-in, depends on core styles, and is idempotent', () => {
  const helper = loadAgentHelper({ coreVersion: '17' })
  assert.equal(helper.document.head.children.length, 0, 'loading the module must not inject styles')

  helper.ensureAgentUIStyles()
  assert.equal(helper.context.__coreCalls, 1)
  assert.deepEqual(helper.document.head.children.map(node => node.id), ['k-agent-ui'])
  assert.equal(helper.document.head.children[0].textContent, 'agent-css\nactivity-css\nconversation-css')
  assert.equal(helper.document.head.children[0].getAttribute('data-faros-agent-ui-version'), '5')

  helper.ensureAgentUIStyles()
  assert.equal(helper.document.head.children.length, 1)
})

test('AgentKit preserves stale style nodes and accepts current or newer hosts', () => {
  const staleNode = styleNode('k-agent-ui', 'stale-agent-css')
  const stale = loadAgentHelper({ coreVersion: '17', agentVersion: '4', existingNodes: [staleNode] })
  stale.ensureAgentUIStyles()
  assert.deepEqual(stale.document.head.children.map(node => node.id), ['k-agent-ui-v5'])
  assert.equal(staleNode.textContent, 'stale-agent-css')

  const current = loadAgentHelper({ coreVersion: '17', agentVersion: '5' })
  current.ensureAgentUIStyles()
  assert.equal(current.document.head.children.length, 0)

  const newer = loadAgentHelper({ coreVersion: '17', agentVersion: '6' })
  newer.ensureAgentUIStyles()
  assert.equal(newer.document.head.children.length, 0)
})
