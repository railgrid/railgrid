<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, nextTick, computed, defineExpose } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useAuthStore } from '@/stores/auth'
import { Wifi, WifiOff, Loader2, RotateCw, Eraser } from 'lucide-vue-next'

const props = defineProps<{
  edgeName: string
  cluster: string
  isActive: boolean
}>()

const auth = useAuthStore()
const termEl = ref<HTMLDivElement | null>(null)
const connectionStatus = ref<'connecting' | 'connected' | 'disconnected' | 'error'>('disconnected')

let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let heartbeatTimer: ReturnType<typeof setInterval> | null = null
let dataDisposable: { dispose: () => void } | null = null
let resizeDisposable: { dispose: () => void } | null = null
let initialized = false
let lifecycle = 0
let disposed = false
const owner = JSON.stringify(auth.user)

// A terminal belongs to the identity that opened it. Disconnect immediately,
// before the parent removes its session row on the next render.
watch(() => JSON.stringify(auth.user), () => {
  disposed = true
  cleanup()
}, { flush: 'sync' })

const statusLabel = computed(() => {
  switch (connectionStatus.value) {
    case 'connecting':
      return 'Connecting…'
    case 'connected':
      return 'Connected'
    case 'disconnected':
      return 'Disconnected'
    case 'error':
      return 'Connection error'
  }
  return 'Unknown'
})

// The edges provider's SSH verb is a kcp custom subresource on its APIExport,
// reached on the hub's kcp front door like any other kube path:
// /clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/linuxservers/{name}/ssh.
function verbPath(verb: string): string {
  return `/clusters/${encodeURIComponent(props.cluster)}/apis/edges.railgrid.ai/v1alpha1/linuxservers/${encodeURIComponent(props.edgeName)}/${verb}`
}

// A browser cannot set an Authorization header on a WebSocket upgrade. The
// Kubernetes convention is to carry the bearer as a subprotocol,
// base64url.bearer.authorization.k8s.io.<base64url token>, which kcp's own
// authenticator (and the hub's front door, for its membership check) reads
// and strips before the request reaches the edges provider. No ticket, no
// second round trip, and the same credential kubectl would present.
//
// That authenticator insists on at least one other subprotocol being offered
// (it removes the bearer one and must still have something to echo), so the
// terminal also offers its own, which the edges SSH handler selects.
const BEARER_SUBPROTOCOL_PREFIX = 'base64url.bearer.authorization.k8s.io.'
const SSH_SUBPROTOCOL = 'railgrid.ssh.v1'

function bearerSubprotocol(token: string): string {
  const bytes = new TextEncoder().encode(token)
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return BEARER_SUBPROTOCOL_PREFIX + btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function buildWsUrl(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}${verbPath('ssh')}`
}

async function initialize() {
  if (disposed || !auth.token || JSON.stringify(auth.user) !== owner || initialized || !termEl.value) return
  const attempt = ++lifecycle
  initialized = true
  connectionStatus.value = 'connecting'

  terminal = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: "'IBM Plex Mono', ui-monospace, Menlo, monospace",
    theme: {
      background: '#0b0c11',
      foreground: '#c8c8d0',
      cursor: '#8b6bff',
      selectionBackground: '#8b6bff33',
      black: '#0b0c11',
      red: '#f87171',
      green: '#34d399',
      yellow: '#fbbf24',
      blue: '#60a5fa',
      magenta: '#a78bfa',
      cyan: '#22d3ee',
      white: '#c8c8d0',
      brightBlack: '#404050',
      brightRed: '#fca5a5',
      brightGreen: '#6ee7b7',
      brightYellow: '#fcd34d',
      brightBlue: '#93c5fd',
      brightMagenta: '#c4b5fd',
      brightCyan: '#67e8f9',
      brightWhite: '#f0f0f5',
    },
  })

  fitAddon = new FitAddon()
  terminal.loadAddon(fitAddon)
  terminal.open(termEl.value)
  fitAddon.fit()

  let subprotocol: string
  try {
    const token = await auth.getValidToken()
    // Logout, unmount, or reconnect can occur while token refresh is pending.
    if (attempt !== lifecycle || disposed || JSON.stringify(auth.user) !== owner) return
    subprotocol = bearerSubprotocol(token)
  } catch {
    if (attempt === lifecycle) cleanup()
    return
  }
  ws = new WebSocket(buildWsUrl(), [subprotocol, SSH_SUBPROTOCOL])
  ws.binaryType = 'arraybuffer'

  ws.onopen = () => {
    connectionStatus.value = 'connected'
    ws!.send(JSON.stringify({ type: 'resize', cols: terminal!.cols, rows: terminal!.rows }))
    heartbeatTimer = setInterval(() => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'heartbeat' }))
    }, 15000)
  }

  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) terminal!.write(new Uint8Array(ev.data))
    else terminal!.write(ev.data)
  }

  ws.onclose = () => {
    connectionStatus.value = 'disconnected'
    terminal?.write('\r\n\x1b[90m--- session ended ---\x1b[0m\r\n')
    stopHeartbeat()
  }

  ws.onerror = () => {
    connectionStatus.value = 'error'
  }

  dataDisposable = terminal.onData((data) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'cmd', cmd: btoa(data) }))
    }
  })

  resizeDisposable = terminal.onResize(({ cols, rows }) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    }
  })
}

function stopHeartbeat() {
  if (heartbeatTimer) {
    clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
}

function resize() {
  nextTick(() => {
    try {
      fitAddon?.fit()
    } catch {
      // FitAddon throws when container is 0x0 (hidden tab); safe to ignore.
    }
  })
}

function focusTerminal() {
  terminal?.focus()
}

function clearTerminal() {
  terminal?.clear()
}

async function reconnect() {
  cleanup()
  initialized = false
  await nextTick()
  await initialize()
}

function cleanup() {
  lifecycle++
  connectionStatus.value = 'disconnected'
  stopHeartbeat()
  dataDisposable?.dispose()
  resizeDisposable?.dispose()
  dataDisposable = null
  resizeDisposable = null
  if (ws) {
    ws.onopen = null
    ws.onmessage = null
    ws.onclose = null
    ws.onerror = null
    if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) ws.close()
  }
  ws = null
  terminal?.dispose()
  terminal = null
  fitAddon = null
}

onMounted(() => {
  if (props.isActive) nextTick(() => initialize())
})

onUnmounted(() => {
  disposed = true
  cleanup()
})

watch(
  () => props.isActive,
  (active) => {
    if (active && !initialized) {
      nextTick(() => initialize())
    } else if (active && initialized) {
      resize()
      focusTerminal()
    }
  },
)

defineExpose({ resize, focusTerminal, clearTerminal, reconnect })
</script>

<template>
  <div class="terminal-instance flex h-full w-full flex-col bg-[#0b0c11]">
    <div class="flex h-7 items-center justify-between gap-2 border-b border-border-subtle bg-surface-overlay/40 px-3 text-[10px] text-text-muted">
      <div class="flex items-center gap-1.5">
        <component
          :is="connectionStatus === 'connected' ? Wifi : connectionStatus === 'connecting' ? Loader2 : WifiOff"
          class="h-3 w-3"
          :class="[
            connectionStatus === 'connected' ? 'text-success' : '',
            connectionStatus === 'connecting' ? 'animate-spin text-warning' : '',
            connectionStatus === 'disconnected' || connectionStatus === 'error' ? 'text-danger' : '',
          ]"
          :stroke-width="1.75"
        />
        <span>{{ statusLabel }}</span>
        <span class="font-mono text-text-muted/50">·</span>
        <span class="font-mono text-text-muted">{{ edgeName }}</span>
      </div>
      <div class="flex items-center gap-1">
        <button
          v-if="connectionStatus === 'disconnected' || connectionStatus === 'error'"
          type="button"
          class="k-btn k-btn--ghost flex h-5 w-5 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-surface-hover hover:text-accent"
          title="Reconnect"
          @click="reconnect"
        >
          <RotateCw class="h-3 w-3" :stroke-width="2" />
        </button>
        <button
          type="button"
          class="k-btn k-btn--ghost flex h-5 w-5 items-center justify-center rounded-md border-0 bg-transparent p-0 text-text-muted transition-colors hover:bg-surface-hover hover:text-accent disabled:opacity-30"
          title="Clear"
          :disabled="connectionStatus !== 'connected'"
          @click="clearTerminal"
        >
          <Eraser class="h-3 w-3" :stroke-width="2" />
        </button>
      </div>
    </div>
    <div ref="termEl" class="min-h-0 flex-1" @click="focusTerminal" />
  </div>
</template>

<style scoped>
:deep(.xterm) {
  padding: 8px;
  height: 100%;
}
:deep(.xterm-viewport) {
  background: transparent !important;
}
</style>
