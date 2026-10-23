import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { App } from 'vue'
import McpAppViewer from '../components/McpAppViewer.vue'
import { MCP_APP_CSP, boundedMcpAppInlineHeight, constrainMcpAppHTML, createMcpAppProxyDocument, mcpAppProviderLabel, safeMcpAppToolInput } from '../mcpAppHost'
import type { AgentArtifactMcpApp } from '../types'

const sdk = vi.hoisted(() => ({ bridges: [] as Array<any>, transports: [] as Array<any> }))

vi.mock('@modelcontextprotocol/ext-apps/app-bridge', () => ({
  AppBridge: class {
    hostInfo: unknown
    capabilities: unknown
    hostContext: unknown
    handlers: Record<string, (...args: unknown[]) => unknown> = {}
    transport: unknown
    onsandboxready?: (...args: unknown[]) => unknown
    oninitialized?: (...args: unknown[]) => unknown
    onsizechange?: (...args: unknown[]) => unknown
    oncalltool?: (...args: unknown[]) => unknown
    onopenlink?: (...args: unknown[]) => unknown
    onmessage?: (...args: unknown[]) => unknown
    ondownloadfile?: (...args: unknown[]) => unknown
    onrequestdisplaymode?: (...args: unknown[]) => unknown
    onupdatemodelcontext?: (...args: unknown[]) => unknown
    onlistresources?: (...args: unknown[]) => unknown
    onlistresourcetemplates?: (...args: unknown[]) => unknown
    onreadresource?: (...args: unknown[]) => unknown
    onlistprompts?: (...args: unknown[]) => unknown
    oncreatesamplingmessage?: (...args: unknown[]) => unknown
    constructor(_client: null, hostInfo: unknown, capabilities: unknown, options: { hostContext: unknown }) {
      this.hostInfo = hostInfo
      this.capabilities = capabilities
      this.hostContext = options.hostContext
      sdk.bridges.push(this)
    }
    async connect(transport: unknown): Promise<void> { this.transport = transport }
    async sendSandboxResourceReady(params: unknown): Promise<void> { sdkMessages.push({ kind: 'resource', value: params }) }
    async sendToolInput(params: unknown): Promise<void> { sdkMessages.push({ kind: 'input', value: params }) }
    async sendToolResult(params: unknown): Promise<void> { sdkMessages.push({ kind: 'result', value: params }) }
    setRequestHandler(method: string, handler: (...args: unknown[]) => unknown): void { this.handlers[method] = handler }
    setHostContext(context: unknown): void { this.hostContext = context }
    async teardownResource(): Promise<void> { sdkMessages.push({ kind: 'teardown' }) }
    async close(): Promise<void> { sdkMessages.push({ kind: 'close' }) }
  },
  PostMessageTransport: class {
    onerror?: (error: Error) => void
    constructor(readonly target: unknown, readonly source: unknown) { sdk.transports.push(this) }
    async close(): Promise<void> { sdkMessages.push({ kind: 'transport-close' }) }
  },
}))

const sdkMessages: Array<{ kind: string; value?: unknown }> = []
const apps: App[] = []
const resultText = '{"amount":9007199254740993.0000000000000001,"rate":1.2300}'
const appResource: AgentArtifactMcpApp = {
  resourceUri: 'ui://semantic-ai/v1/query-result', mimeType: 'text/html;profile=mcp-app',
  html: '<!doctype html><html><head><style>.ok{color:green}</style></head><body><script>window.appReady = true</script></body></html>',
  sha256: 'ab'.repeat(32),
}

function mountViewer(input = '{"query":"select 1","apiKey":"must-not-enter-frame","nested":{"token":"hidden","count":2}}', inline = false) {
  const componentRef = ref<{ teardown?: () => Promise<void> } | null>(null)
  const root = defineComponent({
    setup() {
      return () => h(McpAppViewer, { ref: componentRef, app: appResource, resultData: resultText, input, inline })
    },
  })
  const element = document.createElement('div')
  document.body.appendChild(element)
  const app = createApp(root)
  app.mount(element)
  apps.push(app)
  return { app, element, componentRef }
}

async function settle(): Promise<void> {
  for (let i = 0; i < 8; i += 1) { await nextTick(); await Promise.resolve() }
}

afterEach(() => {
  while (apps.length) apps.pop()?.unmount()
  sdk.bridges.length = 0
  sdk.transports.length = 0
  sdkMessages.length = 0
  vi.unstubAllGlobals()
  document.body.replaceChildren()
})

describe('MCP Apps read-only host boundary', () => {
  it('derives generic provider attribution and bounds inline content height', () => {
    expect(mcpAppProviderLabel('ui://semantic-ai/v1/query-result')).toBe('Semantic AI')
    expect(mcpAppProviderLabel('ui://acme-bi/reports')).toBe('Acme BI')
    expect(mcpAppProviderLabel('ui://railgrid-provider/semantic-ai/base64')).toBe('Semantic AI')
    expect(mcpAppProviderLabel('ui://railgrid-provider/acme-bi/base64')).toBe('Acme BI')
    expect(boundedMcpAppInlineHeight(32)).toBe(96)
    expect(boundedMcpAppInlineHeight(240)).toBe(240)
    expect(boundedMcpAppInlineHeight(2_000)).toBe(1_600)
    expect(boundedMcpAppInlineHeight(Number.POSITIVE_INFINITY)).toBeNull()
  })

  it('constrains provider HTML and supplies an opaque double-iframe proxy with no network route', () => {
    const appHTML = '<!doctype html><script>start()</script><html><head></head><body></body></html>'
    const constrained = constrainMcpAppHTML(appHTML)
    const proxy = createMcpAppProxyDocument()

    expect(constrained.indexOf('Content-Security-Policy')).toBeLessThan(constrained.indexOf('start()'))
    expect(constrained.match(/<!doctype/gi)).toHaveLength(1)
    expect(constrained).toContain("connect-src 'none'")
    expect(constrained).toContain("frame-src 'none'")
    expect(MCP_APP_CSP).toContain("form-action 'none'")
    expect(proxy).toContain('ui/notifications/sandbox-proxy-ready')
    expect(proxy).toContain('ui/notifications/sandbox-resource-ready')
    expect(proxy).toContain("frame.setAttribute('sandbox', 'allow-scripts')")
    expect(proxy).toContain("connect-src 'none'")
    expect(proxy).not.toMatch(/localStorage|sessionStorage|fetch\(|XMLHttpRequest|Authorization|Bearer/i)
    expect(safeMcpAppToolInput('{"query":"select 1","apiKey":"secret","nested":{"token":"hidden","count":2}}'))
      .toEqual({ query: 'select 1', nested: { count: 2 } })
    expect(safeMcpAppToolInput('not-json')).toBeUndefined()
  })

  it('initializes via AppBridge, sends only redacted input and exact result content, and rejects unsupported requests', async () => {
    vi.stubGlobal('crypto', { subtle: { digest: async () => new Uint8Array(32).fill(0xab).buffer } })
    const view = mountViewer()
    await settle()
    const bridge = sdk.bridges[0]!
    const iframe = view.element.querySelector('iframe')!
    expect(bridge.hostInfo).toEqual({ name: 'Railgrid Agents', version: '0.1.0' })
    expect(bridge.capabilities).toEqual({})
    expect(bridge.hostContext).toMatchObject({ displayMode: 'fullscreen' })
    expect(JSON.stringify(bridge.hostContext)).not.toMatch(/tenant|token|authorization|secret|user/i)
    expect(iframe.getAttribute('sandbox')).toBe('allow-scripts')
    expect(iframe.hasAttribute('allow')).toBe(false)
    expect(iframe.getAttribute('referrerpolicy')).toBe('no-referrer')
    expect(iframe.hasAttribute('credentialless')).toBe(true)

    await bridge.onsandboxready?.({})
    await bridge.oninitialized?.({})
    await settle()
    const input = sdkMessages.find(message => message.kind === 'input')?.value as { arguments?: Record<string, unknown> }
    const result = sdkMessages.find(message => message.kind === 'result')?.value as Record<string, unknown>
    expect(input.arguments).toEqual({ query: 'select 1', nested: { count: 2 } })
    expect(JSON.stringify(input)).not.toMatch(/apiKey|token|must-not-enter-frame|hidden/)
    expect(result).toEqual({ content: [{ type: 'text', text: resultText }] })
    expect(JSON.stringify(result)).not.toContain('structuredContent')
    expect(sdkMessages.find(message => message.kind === 'resource')?.value).toMatchObject({ sandbox: 'allow-scripts' })

    expect(await bridge.oncalltool?.({ name: 'run_query' })).toMatchObject({ isError: true })
    expect(await bridge.onopenlink?.({ url: 'https://example.test' })).toMatchObject({ isError: true })
    expect(await bridge.onmessage?.({ role: 'user', content: [] })).toMatchObject({ isError: true })
    expect(await bridge.ondownloadfile?.({ contents: [] })).toMatchObject({ isError: true })
    await expect(Promise.resolve(bridge.handlers['tools/list']?.({}))).rejects.toThrow('not available')
    await expect(Promise.resolve(bridge.onreadresource?.({ uri: 'file:///etc/passwd' }))).rejects.toThrow('not available')
  })

  it('reports inline display mode and applies bounded provider resize notifications without feedback', async () => {
    vi.stubGlobal('crypto', { subtle: { digest: async () => new Uint8Array(32).fill(0xab).buffer } })
    const view = mountViewer('{}', true)
    await settle()
    const bridge = sdk.bridges[0]!
    expect(bridge.hostContext).toMatchObject({ displayMode: 'inline', containerDimensions: { height: 240 } })

    await bridge.onsizechange?.({ width: 320, height: 40 })
    await settle()
    expect(view.element.firstElementChild?.getAttribute('style')).toContain('height: 96px')
    expect(bridge.hostContext).toMatchObject({ displayMode: 'inline', containerDimensions: { height: 96 } })

    await bridge.onsizechange?.({ width: 320, height: 9_000 })
    await settle()
    expect(view.element.firstElementChild?.getAttribute('style')).toContain('height: 1600px')
    expect(bridge.hostContext).toMatchObject({ displayMode: 'inline', containerDimensions: { height: 1600 } })

    await bridge.onsizechange?.({ width: 320, height: Number.NaN })
    await settle()
    expect(view.element.firstElementChild?.getAttribute('style')).toContain('height: 1600px')
    expect(bridge.hostContext).toMatchObject({ displayMode: 'inline', containerDimensions: { height: 1600 } })
  })

  it('tears down and removes the frame before a reopened viewer creates a fresh bridge', async () => {
    vi.stubGlobal('crypto', { subtle: { digest: async () => new Uint8Array(32).fill(0xab).buffer } })
    const first = mountViewer()
    await settle()
    const firstBridge = sdk.bridges[0]!
    await firstBridge.oninitialized?.({})
    await settle()
    await first.componentRef.value?.teardown?.()
    expect(sdkMessages.some(message => message.kind === 'teardown')).toBe(true)
    expect(sdkMessages.some(message => message.kind === 'close')).toBe(true)
    expect(sdkMessages.some(message => message.kind === 'transport-close')).toBe(true)
    await settle()
    expect(first.element.querySelector('iframe')).toBeNull()

    first.app.unmount()
    const second = mountViewer('{}')
    await settle()
    expect(sdk.bridges).toHaveLength(2)
    expect(sdk.bridges[1]).not.toBe(firstBridge)
    await sdk.bridges[1]!.oninitialized?.({})
    await settle()
    expect(sdkMessages.filter(message => message.kind === 'result')).toHaveLength(2)
    await second.componentRef.value?.teardown?.()
  })
})
