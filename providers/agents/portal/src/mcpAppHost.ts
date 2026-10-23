import type { AgentArtifactMcpApp } from './types'

export const MCP_APP_MAX_HTML_BYTES = 1 << 20
export const MCP_APP_INLINE_MIN_HEIGHT = 96
export const MCP_APP_INLINE_MAX_HEIGHT = 1600

export function boundedMcpAppInlineHeight(value: unknown): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value)) return null
  return Math.max(MCP_APP_INLINE_MIN_HEIGHT, Math.min(MCP_APP_INLINE_MAX_HEIGHT, Math.ceil(value)))
}

/** Use the validated MCP resource authority as generic provider attribution. */
export function mcpAppProviderLabel(resourceUri: string): string {
  let url: URL
  try { url = new URL(resourceUri) } catch { return 'Provider' }
  let authority = url.hostname
  // Federated MCP resource URIs use a platform authority and put the
  // provider namespace in the first path segment (ui://railgrid-provider/<provider>/...).
  if (url.protocol === 'ui:' && authority.toLowerCase() === 'railgrid-provider') {
    authority = url.pathname.split('/').filter(Boolean)[0] || ''
  }
  const acronyms = new Set(['ai', 'api', 'bi', 'mcp', 'sql', 'ui'])
  const words = authority.replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(/[._-]+/).filter(Boolean)
  if (!words.length) return 'Provider'
  return words.map(word => acronyms.has(word.toLowerCase())
    ? word.toUpperCase()
    : `${word.charAt(0).toUpperCase()}${word.slice(1)}`).join(' ')
}

export const MCP_APP_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline'",
  "style-src 'unsafe-inline'",
  'img-src data: blob:',
  'font-src data:',
  "connect-src 'none'",
  "media-src 'none'",
  "object-src 'none'",
  "frame-src 'none'",
  "child-src 'none'",
  "worker-src 'none'",
  "manifest-src 'none'",
  "form-action 'none'",
  "base-uri 'none'",
].join('; ')

const PROXY_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline'",
  "style-src 'unsafe-inline'",
  "frame-src 'self' about:",
  "child-src 'self' about:",
  "connect-src 'none'",
  'img-src data: blob:',
  'font-src data:',
  "media-src 'none'",
  "worker-src 'none'",
  "manifest-src 'none'",
  "object-src 'none'",
  "form-action 'none'",
  "base-uri 'none'",
].join('; ')

function cspMeta(policy: string): string {
  return `<meta http-equiv="Content-Security-Policy" content="${policy}">`
}

/** Add a stricter policy ahead of provider code; policy headers only combine restrictively. */
export function constrainMcpAppHTML(html: string): string {
  const meta = cspMeta(MCP_APP_CSP)
  const body = html.replace(/^\s*<!doctype\s+html\s*>/i, '')
  return `<!doctype html>${meta}${body}`
}

/** Static opaque-origin proxy. It relays only standard JSON-RPC postMessage traffic. */
export function createMcpAppProxyDocument(): string {
  const policy = cspMeta(PROXY_CSP)
  return `<!doctype html><html><head>${policy}<meta charset="utf-8"><style>html,body{width:100%;height:100%;margin:0;overflow:hidden}iframe{position:fixed;inset:0;width:100%;height:100%;margin:0;border:0;background:transparent}</style></head><body><script>
(() => {
  'use strict';
  let appFrame = null;
  let appWindow = null;
  let resourceLoaded = false;
  const isRPC = value => value && value.jsonrpc === '2.0' &&
    (typeof value.method === 'string' || typeof value.id === 'string' || typeof value.id === 'number');
  const denyFrameOptions = frame => {
    frame.setAttribute('sandbox', 'allow-scripts');
    frame.setAttribute('referrerpolicy', 'no-referrer');
    frame.setAttribute('title', 'Provider result app');
    frame.setAttribute('aria-label', 'Provider result app');
    frame.style.cssText = 'position:fixed;inset:0;width:100%;height:100%;margin:0;border:0;background:transparent';
  };
  window.addEventListener('message', event => {
    if (event.source === parent) {
      const message = event.data;
      if (!isRPC(message)) return;
      if (message.method === 'ui/notifications/sandbox-resource-ready') {
        if (resourceLoaded || typeof message.params?.html !== 'string' || message.params.sandbox !== 'allow-scripts') return;
        resourceLoaded = true;
        appFrame = document.createElement('iframe');
        denyFrameOptions(appFrame);
        appFrame.srcdoc = message.params.html;
        appFrame.addEventListener('load', () => { appWindow = appFrame.contentWindow; }, { once: true });
        document.body.appendChild(appFrame);
        appWindow = appFrame.contentWindow;
        return;
      }
      if (appWindow) appWindow.postMessage(message, '*');
      return;
    }
    if (appWindow && event.source === appWindow && isRPC(event.data)) parent.postMessage(event.data, '*');
  });
  window.addEventListener('load', () => {
    parent.postMessage({ jsonrpc: '2.0', method: 'ui/notifications/sandbox-proxy-ready', params: {} }, '*');
  }, { once: true });
})();
</script></body></html>`
}

/** Defense in depth for already-redacted recorded arguments. */
export function safeMcpAppToolInput(input: string | undefined): Record<string, unknown> | undefined {
  if (!input || new TextEncoder().encode(input).byteLength > 16 * 1024) return undefined
  let parsed: unknown
  try { parsed = JSON.parse(input) as unknown } catch { return undefined }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return undefined
  const clean = (value: unknown, depth: number): unknown => {
    if (depth > 12) return undefined
    if (Array.isArray(value)) return value.slice(0, 256).map(item => clean(item, depth + 1))
    if (value && typeof value === 'object') {
      const result: Record<string, unknown> = {}
      for (const [key, child] of Object.entries(value).slice(0, 256)) {
        if (['__proto__', 'constructor', 'prototype'].includes(key) || /authorization|bearer|token|secret|password|credential|api[-_]?key/i.test(key)) continue
        const nested = clean(child, depth + 1)
        if (nested !== undefined) result[key] = nested
      }
      return result
    }
    if (typeof value === 'string') return value.slice(0, 16 * 1024)
    if (typeof value === 'number' || typeof value === 'boolean' || value === null) return value
    return undefined
  }
  return clean(parsed, 0) as Record<string, unknown>
}

export async function verifyMcpAppHTML(app: AgentArtifactMcpApp): Promise<boolean> {
  if (new TextEncoder().encode(app.html).byteLength > MCP_APP_MAX_HTML_BYTES || !globalThis.crypto?.subtle) return false
  const digest = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(app.html))
  const hex = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
  return hex === app.sha256
}
