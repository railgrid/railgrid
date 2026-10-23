<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AppBridge, PostMessageTransport } from '@modelcontextprotocol/ext-apps/app-bridge'
import type { AgentArtifactAppIdentity, AgentArtifactMcpApp, RailgridContext } from '../types'
import { boundedMcpAppInlineHeight, constrainMcpAppHTML, mcpAppProxyURL, safeMcpAppToolInput, verifyMcpAppHTML } from '../mcpAppHost'
import { mcpAppIdentityMatches } from '../artifactIntegrity'
import { useAgentsRuntime } from '../vue/runtime'

const props = defineProps<{
  app: AgentArtifactMcpApp
  appIdentity?: AgentArtifactAppIdentity
  resultData: string
  input?: string
  inline?: boolean
}>()
const emit = defineEmits<{ failure: []; resize: [] }>()

const runtime = useAgentsRuntime()
const container = ref<HTMLElement | null>(null)
const frame = ref<HTMLIFrameElement | null>(null)
const frameMounted = ref(true)
const ready = ref(false)
const inlineHeight = ref(240)
let bridge: AppBridge | null = null
let transport: PostMessageTransport | null = null
let timeout: ReturnType<typeof setTimeout> | undefined
let resizeObserver: ResizeObserver | undefined
let themeObserver: MutationObserver | undefined
let tornDown = false
let dataSent = false
let teardownFlight: Promise<void> | null = null
let lastHostContext = ''
let inlineResizeEcho: { width: number; height: number } | null = null

function hostElement(): (HTMLElement & { railgridContext?: RailgridContext | null }) | null {
  return runtime?.host.value as (HTMLElement & { railgridContext?: RailgridContext | null }) | null
}

function currentTheme(): 'light' | 'dark' {
  const host = hostElement()
  const requested = host?.railgridContext?.theme
  if (requested === 'light' || requested === 'dark') return requested
  const elements = [host, document.documentElement].filter((item): item is HTMLElement => !!item)
  for (const element of elements) {
    const signal = `${element.getAttribute('data-theme') || ''} ${element.className || ''}`.toLowerCase()
    if (/(^|\s|-)dark($|\s|-)/.test(signal)) return 'dark'
    if (/(^|\s|-)light($|\s|-)/.test(signal)) return 'light'
  }
  // For a system theme, inspect the rendered host CSS rather than asking the
  // browser preference directly. The host's computed color-scheme is its
  // effective theme contract for this surface.
  const scheme = getComputedStyle(host || document.documentElement).colorScheme.toLowerCase()
  if (scheme === 'dark') return 'dark'
  if (scheme === 'light') return 'light'
  const surface = getComputedStyle(host || document.documentElement).getPropertyValue('--color-surface').trim()
  const rgb = surface.match(/^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/i)
  if (rgb) {
    const channels = rgb.slice(1, 4).map(value => Number(value) / 255).map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
    if (channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722 < 0.35) return 'dark'
  }
  return 'light'
}

function hostContext(): { theme: 'light' | 'dark'; displayMode: 'inline' | 'fullscreen'; containerDimensions: { width: number; height: number } } {
  const rect = container.value?.getBoundingClientRect()
  return {
    theme: currentTheme(),
    displayMode: props.inline ? 'inline' : 'fullscreen',
    containerDimensions: {
      width: Math.max(1, Math.round(rect?.width || 720)),
      height: Math.max(1, Math.round(rect?.height || (props.inline ? inlineHeight.value : 480))),
    },
  }
}

function updateHostContext(): void {
  if (!bridge || tornDown) return
  const context = hostContext()
  const key = JSON.stringify(context)
  if (key === lastHostContext) return
  lastHostContext = key
  bridge.setHostContext(context)
}

function onResizeObserved(): void {
  if (props.inline && inlineResizeEcho && container.value) {
    const rect = container.value.getBoundingClientRect()
    const width = Math.max(1, Math.round(rect.width || 720))
    const height = Math.max(1, Math.round(rect.height || inlineHeight.value))
    const expected = inlineResizeEcho
    inlineResizeEcho = null
    if (Math.abs(height - expected.height) <= 1 && Math.abs(width - expected.width) <= 1) return
  }
  updateHostContext()
}

function rejectUnsupportedCapabilities(instance: AppBridge): void {
  // The host advertises no capabilities. These handlers make requests that
  // exceed the read-only POC fail explicitly instead of reaching a provider,
  // chat, navigation surface, or host storage.
  const reject = async (): Promise<never> => { throw new Error('This provider view capability is not available.') }
  instance.oncalltool = async () => ({ isError: true, content: [{ type: 'text', text: 'Tool calls are unavailable in this read-only view.' }] })
  instance.onopenlink = async () => ({ isError: true })
  instance.onmessage = async () => ({ isError: true })
  instance.ondownloadfile = async () => ({ isError: true })
  instance.onrequestdisplaymode = reject
  instance.onupdatemodelcontext = reject
  instance.setRequestHandler('tools/list', reject)
  instance.onlistresources = reject
  instance.onlistresourcetemplates = reject
  instance.onreadresource = reject
  instance.onlistprompts = reject
  instance.oncreatesamplingmessage = reject
}

async function sendReadOnlyData(instance: AppBridge): Promise<void> {
  if (dataSent || tornDown) return
  dataSent = true
  const argumentsValue = safeMcpAppToolInput(props.input)
  await instance.sendToolInput(argumentsValue ? { arguments: argumentsValue } : {})
  if (tornDown) return
  // Keep the exact captured result text as the sole content block. Do not
  // parse it into structuredContent, which can round large JSON numbers.
  await instance.sendToolResult({ content: [{ type: 'text', text: props.resultData }] })
  ready.value = true
  if (timeout) clearTimeout(timeout)
  timeout = undefined
}

function fail(): void {
  if (tornDown) return
  emit('failure')
  void teardown()
}

async function start(): Promise<void> {
  const frameWindow = frame.value?.contentWindow
  if (!frameWindow) return fail()
  try {
    if (!mcpAppIdentityMatches(props.app, props.appIdentity) || !await verifyMcpAppHTML(props.app, props.appIdentity?.sha256)) return fail()
    const proxyURL = await mcpAppProxyURL(hostElement()?.railgridContext?.basePath)
    if (!proxyURL || tornDown) return fail()
    const instance = new AppBridge(
      null,
      { name: 'Railgrid Agents', version: '0.1.0' },
      {},
      { hostContext: hostContext() },
    )
    bridge = instance
    rejectUnsupportedCapabilities(instance)
    instance.onsandboxready = async () => {
      if (tornDown) return
      await instance.sendSandboxResourceReady({ html: constrainMcpAppHTML(props.app.html), sandbox: 'allow-scripts' })
    }
    instance.oninitialized = () => { void sendReadOnlyData(instance).catch(fail) }
    instance.onsizechange = size => {
      if (!props.inline) return
      const nextHeight = boundedMcpAppInlineHeight(size.height)
      if (nextHeight === null || Math.abs(nextHeight - inlineHeight.value) < 8) return
      const rect = container.value?.getBoundingClientRect()
      inlineResizeEcho = {
        width: Math.max(1, Math.round(rect?.width || 720)),
        height: nextHeight,
      }
      inlineHeight.value = nextHeight
      void nextTick(updateHostContext)
      emit('resize')
    }
    transport = new PostMessageTransport(frameWindow, frameWindow)
    transport.onerror = fail
    await instance.connect(transport)
    if (tornDown) return
    frame.value!.src = proxyURL
    timeout = setTimeout(fail, 15_000)
    resizeObserver = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(onResizeObserved)
    if (container.value) resizeObserver?.observe(container.value)
    themeObserver = new MutationObserver(updateHostContext)
    for (const element of [document.documentElement, hostElement()].filter((item): item is HTMLElement => !!item)) {
      themeObserver.observe(element, { attributes: true, attributeFilter: ['class', 'style', 'data-theme'] })
    }
  } catch {
    fail()
  }
}

async function teardown(): Promise<void> {
  if (teardownFlight) return teardownFlight
  teardownFlight = (async () => {
    if (tornDown) return
    tornDown = true
    if (timeout) clearTimeout(timeout)
    timeout = undefined
    resizeObserver?.disconnect()
    themeObserver?.disconnect()
    if (bridge && ready.value) {
      try {
        await Promise.race([
          bridge.teardownResource({}, { timeout: 700 }).then(() => undefined),
          new Promise<void>(resolve => setTimeout(resolve, 750)),
        ])
      } catch { /* a timed-out view is still removed below */ }
    }
    try { await bridge?.close() } catch { /* transport may already be closed */ }
    try { await transport?.close() } catch { /* transport may already be closed */ }
    bridge = null
    transport = null
    frameMounted.value = false
    await nextTick()
  })()
  return teardownFlight
}

watch(() => [props.app.sha256, props.appIdentity?.provider, props.appIdentity?.resourceUri, props.appIdentity?.sha256, props.resultData, props.input] as const, () => {
  if (!tornDown) fail()
})

onMounted(() => { void start() })
onBeforeUnmount(() => { void teardown() })

defineExpose({ teardown })
</script>

<template>
  <div ref="container" class="agents-mcp-app" :class="{ 'agents-mcp-app--inline': inline }" :style="inline ? { height: `${inlineHeight}px` } : undefined" aria-label="Provider result app">
    <iframe
      v-if="frameMounted"
      ref="frame"
      class="agents-mcp-app__proxy"
      title="Provider result app"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      credentialless
      aria-label="Provider result app"
    />
    <span v-if="!ready" class="agents-mcp-app__loading" role="status">Opening provider view…</span>
  </div>
</template>
