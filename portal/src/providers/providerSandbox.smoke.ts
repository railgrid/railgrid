import {
  createProviderSandboxFrame,
  verifyProviderBundleIntegrity,
  type IsolatedProviderContext,
} from './providerSandbox'

const result = document.querySelector<HTMLPreElement>('#result')!
const iframe = document.querySelector<HTMLIFrameElement>('#provider')!
const nonce = crypto.randomUUID()
const report: Record<string, string> = {}
let tokenReads = 0

function finish(): void {
  const checks = [
    report.parentStorage === 'SecurityError',
    report.opaqueStorage === 'SecurityError',
    report.contextToken === 'absent',
    report.fetchType === 'function',
    report.bridgeRequest === 'SecurityError',
    report.directNetwork === 'TypeError',
    tokenReads === 0,
  ]
  result.textContent = `${checks.every(Boolean) ? 'PASS' : 'FAIL'}\n${JSON.stringify({ ...report, tokenReads }, null, 2)}`
}

window.addEventListener('message', event => {
  if (event.source !== iframe.contentWindow || event.origin !== 'null' ||
      event.data?.type !== 'sandbox-smoke-report' || event.data?.nonce !== nonce) return
  Object.assign(report, event.data.report)
  finish()
})

const context: IsolatedProviderContext = {
  providerName: 'hostile-smoke',
  apiGroups: [],
  dependencies: [],
  tenant: 'smoke-cluster',
  orgUUID: 'smoke-org',
  workspaceUUID: 'smoke-workspace',
  theme: 'light',
  user: null,
  navigationBasePath: '/ui/providers/hostile-smoke',
  basePath: '/ui/providers/hostile-smoke',
}
const hostileSource = `customElements.define('railgrid-provider-hostile-smoke', class extends HTMLElement {
  connectedCallback() {
    setTimeout(async () => {
      const report = {}
      try { parent.localStorage.getItem('x'); report.parentStorage = 'accessible' }
      catch (error) { report.parentStorage = error.name }
      try { localStorage.getItem('x'); report.opaqueStorage = 'accessible' }
      catch (error) { report.opaqueStorage = error.name }
      report.contextToken = ('token' in this.railgridContext) ? this.railgridContext.token : 'absent'
      report.fetchType = typeof this.railgridContext.fetch
      try { await this.railgridContext.fetch('/api/orgs/another-org/admin/users'); report.bridgeRequest = 'allowed' }
      catch (error) { report.bridgeRequest = error.name }
      try { await fetch('https://example.com/'); report.directNetwork = 'allowed' }
      catch (error) { report.directNetwork = error.name }
      parent.postMessage({ type: 'sandbox-smoke-report', nonce: '${nonce}', report }, '*')
    }, 0)
  }
})`
const sourceBytes = new TextEncoder().encode(hostileSource)
const bundleBytes = sourceBytes.buffer.slice(sourceBytes.byteOffset, sourceBytes.byteOffset + sourceBytes.byteLength)
const digest = await crypto.subtle.digest('SHA-384', bundleBytes)
const digestBase64 = btoa(String.fromCharCode(...new Uint8Array(digest)))
const integrity = `sha384-${digestBase64}`
if (!await verifyProviderBundleIntegrity(bundleBytes, integrity)) {
  result.textContent = 'FAIL\nCould not verify the test bundle bytes.'
} else {
  createProviderSandboxFrame(iframe, {
    bytes: bundleBytes,
    assetBase: '/ui/providers/hostile-smoke/main.js?grant=smoke-only',
  }, context, {
    isCurrent: () => true,
    scope: () => ({ tenant: context.tenant, orgUUID: context.orgUUID, workspaceUUID: context.workspaceUUID }),
    getToken: async () => { tokenReads++; return 'smoke-only-bearer' },
    onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
    onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    onReady() {},
    onError(error) { result.textContent = `FAIL\n${error.message}` },
  })
}
