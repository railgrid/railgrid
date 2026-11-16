/*
 * Copyright 2026 The Railgrid Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

export const PREVIEW_BRIDGE_PROTOCOL_VERSION = 1

export const PREVIEW_BRIDGE_ANNOTATION_START = 'railgrid.preview-bridge.annotation.start'
export const PREVIEW_BRIDGE_ANNOTATION_STOP = 'railgrid.preview-bridge.annotation.stop'
export const PREVIEW_BRIDGE_ANNOTATION_PINS = 'railgrid.preview-bridge.annotation.pins'
export const PREVIEW_BRIDGE_ANNOTATION_PINS_RENDERED = 'railgrid.preview-bridge.annotation.pins-rendered'
export const PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER = 'railgrid.preview-bridge.annotation.pin-hover'
export const PREVIEW_BRIDGE_ANNOTATION_PIN_SELECTED = 'railgrid.preview-bridge.annotation.pin-selected'
export const PREVIEW_BRIDGE_ANNOTATION_SELECTED = 'railgrid.preview-bridge.annotation.selected'
export const PREVIEW_BRIDGE_ANNOTATION_CANCELLED = 'railgrid.preview-bridge.annotation.cancelled'
export const PREVIEW_BRIDGE_ANNOTATION_MODE = 'railgrid.preview-bridge.annotation.mode'
export const PREVIEW_BRIDGE_MAX_ANNOTATION_PINS = 64

export type PreviewBridgeConnectionState =
  | 'disabled'
  | 'connecting'
  | 'connected'
  | 'unavailable'

/**
 * One bridge session is one MessagePort bound to one preview document. The
 * portal mints the session ID itself; there is no server-side session. Trust
 * comes from the browser: the portal accepts READY only from the preview
 * iframe's window on the preview origin, and the injected bridge answers only
 * a parent on a platform-configured portal origin.
 */
interface PreviewBridgeSession {
  sessionID: string
  generation: string
}

export interface PreviewBridgeAnnotationRect {
  x: number
  y: number
  width: number
  height: number
}

export interface PreviewBridgeAnnotationTarget {
  tag?: string
  role?: string
  name?: string
  text?: string
  locator?: string
  locatorStrategy?: string
  ancestors?: string[]
  rect?: PreviewBridgeAnnotationRect
}

export interface PreviewBridgeAnnotationSelection {
  documentID: string
  pagePath: string
  viewport: { width: number; height: number }
  target: PreviewBridgeAnnotationTarget
  anchor?: { x: number; y: number }
}

export interface PreviewBridgeAnnotationPin {
  id: string
  number: number
  documentID: string
  pagePath: string
  boundingRect: PreviewBridgeAnnotationRect
  target: PreviewBridgeAnnotationTarget
  anchor?: { x: number; y: number }
}

export interface PreviewBridgeAnnotationPinRenderState {
  id: string
  resolved: boolean
}

export interface PreviewBridgeAnnotationPinHover {
  id: string
  active: boolean
  pagePath: string
  rect: PreviewBridgeAnnotationRect
}

export interface PreviewBridgeAnnotationPinSelection {
  id: string
  pagePath: string
  rect: PreviewBridgeAnnotationRect
  viewport: { width: number; height: number }
}

interface PreviewBridgeControllerOptions {
  getFrame: () => HTMLIFrameElement | null
  onState: (state: PreviewBridgeConnectionState) => void
  onAnnotation?: (selection: PreviewBridgeAnnotationSelection) => void
  onAnnotationMode?: (active: boolean) => void
  onAnnotationPinsRendered?: (documentID: string, pagePath: string, states: PreviewBridgeAnnotationPinRenderState[]) => void
  onAnnotationPinHover?: (hover: PreviewBridgeAnnotationPinHover) => void
  onAnnotationPinSelect?: (selection: PreviewBridgeAnnotationPinSelection) => void
  onDocument?: (documentID: string, pagePath: string) => void
}

interface BridgeReadyMessage {
  type: 'railgrid.preview-bridge.ready'
  version: number
  documentID?: string
  path?: string
}

interface BridgePortMessage {
  type: string
  version: number
  sessionID: string
  generation: string
  documentID?: string
  path?: string
  active?: boolean
  viewport?: unknown
  target?: unknown
  anchor?: unknown
  pins?: unknown
  id?: unknown
  rect?: unknown
}

export class PreviewBridgeController {
  private readonly getFrame: () => HTMLIFrameElement | null
  private readonly onState: (state: PreviewBridgeConnectionState) => void
  private readonly onAnnotation?: (selection: PreviewBridgeAnnotationSelection) => void
  private readonly onAnnotationMode?: (active: boolean) => void
  private readonly onAnnotationPinsRendered?: (documentID: string, pagePath: string, states: PreviewBridgeAnnotationPinRenderState[]) => void
  private readonly onAnnotationPinHover?: (hover: PreviewBridgeAnnotationPinHover) => void
  private readonly onAnnotationPinSelect?: (selection: PreviewBridgeAnnotationPinSelection) => void
  private readonly onDocument?: (documentID: string, pagePath: string) => void
  private project = ''
  private session: PreviewBridgeSession | null = null
  private port: MessagePort | null = null
  private connectionTimer: number | undefined
  private started = false
  private readyGeneration = ''
  private expectedOrigin = ''
  private serial = 0
  private annotationMode = false
  private bridgeConnected = false
  private annotationPins: PreviewBridgeAnnotationPin[] = []

  constructor(options: PreviewBridgeControllerOptions) {
    this.getFrame = options.getFrame
    this.onState = options.onState
    this.onAnnotation = options.onAnnotation
    this.onAnnotationMode = options.onAnnotationMode
    this.onAnnotationPinsRendered = options.onAnnotationPinsRendered
    this.onAnnotationPinHover = options.onAnnotationPinHover
    this.onAnnotationPinSelect = options.onAnnotationPinSelect
    this.onDocument = options.onDocument
    window.addEventListener('message', this.handleWindowMessage)
  }

  async connect(project: string): Promise<void> {
    const serial = ++this.serial
    const projectChanged = project !== this.project
    this.closeSession()
    if (projectChanged) this.annotationPins = []
    this.readyGeneration = ''
    this.project = project
    if (!project || !this.getFrame()?.contentWindow) {
      this.onState('unavailable')
      return
    }
    this.onState('connecting')
    try {
      this.expectedOrigin = new URL(this.getFrame()?.src ?? '').origin
      this.connectionTimer = window.setTimeout(() => {
        this.connectionTimer = undefined
        if (serial !== this.serial) return
        this.readyGeneration = ''
        this.onState('unavailable')
        this.closeSession()
      }, 3_000)
      // Probe after the iframe load so an early bootstrap-ready message cannot
      // be missed. The bridge's document ID is the immutable generation; START
      // must echo it, so a session can never bind to a predecessor document.
      this.getFrame()?.contentWindow?.postMessage({
        type: 'railgrid.preview-bridge.probe',
        version: PREVIEW_BRIDGE_PROTOCOL_VERSION,
      }, this.expectedOrigin)
    } catch {
      if (serial === this.serial) this.onState('unavailable')
    }
  }

  reconnect(): Promise<void> {
    const project = this.project
    if (!project) {
      this.onState('unavailable')
      return Promise.resolve()
    }
    return this.connect(project)
  }

  async disconnect(): Promise<void> {
    ++this.serial
    this.closeSession()
    this.project = ''
    this.readyGeneration = ''
    this.annotationPins = []
    this.onState('disabled')
  }

  /** Start element-pick mode only after the bridge is connected. */
  startAnnotationMode(): boolean {
    const session = this.session
    const port = this.port
    if (!session || !port || !this.started || !this.bridgeConnected || !this.expectedOrigin) return false
    try {
      port.postMessage({
        type: PREVIEW_BRIDGE_ANNOTATION_START,
        version: PREVIEW_BRIDGE_PROTOCOL_VERSION,
        sessionID: session.sessionID,
        generation: session.generation,
      })
      this.annotationMode = true
      this.onAnnotationMode?.(true)
      return true
    } catch {
      this.handlePortFailure(port)
      return false
    }
  }

  stopAnnotationMode(): void {
    const session = this.session
    const port = this.port
    if (session && port) {
      try {
        port.postMessage({
          type: PREVIEW_BRIDGE_ANNOTATION_STOP,
          version: PREVIEW_BRIDGE_PROTOCOL_VERSION,
          sessionID: session.sessionID,
          generation: session.generation,
        })
      } catch {
        this.handlePortFailure(port)
      }
    }
    if (this.annotationMode) this.onAnnotationMode?.(false)
    this.annotationMode = false
  }

  setAnnotationPins(pins: PreviewBridgeAnnotationPin[]): boolean {
    if (!Array.isArray(pins) || pins.length > PREVIEW_BRIDGE_MAX_ANNOTATION_PINS) return false
    const nextPins = pins.map((pin) => ({
      id: pin.id,
      number: pin.number,
      documentID: pin.documentID,
      pagePath: pin.pagePath,
      // Composer parts are Vue-reactive. MessagePort's structured clone
      // rejects Proxy objects, so make the bridge boundary explicitly plain.
      boundingRect: clonePreviewBridgeAnnotationRect(pin.boundingRect),
      target: clonePreviewBridgeAnnotationTarget(pin.target),
      ...(pin.anchor ? { anchor: { x: pin.anchor.x, y: pin.anchor.y } } : {}),
    }))
    if (previewBridgeAnnotationPinsEqual(this.annotationPins, nextPins)) {
      return Boolean(this.session && this.port && this.started && this.bridgeConnected)
    }
    this.annotationPins = nextPins
    return this.postAnnotationPins()
  }

  private postAnnotationPins(): boolean {
    const session = this.session
    const port = this.port
    if (!session || !port || !this.started || !this.bridgeConnected) return false
    try {
      port.postMessage({
        type: PREVIEW_BRIDGE_ANNOTATION_PINS,
        version: PREVIEW_BRIDGE_PROTOCOL_VERSION,
        sessionID: session.sessionID,
        generation: session.generation,
        // Pin provenance retains the document that was annotated, while the
        // transport is rebound to the currently connected preview
        // generation. The bridge still requires an exact pagePath and unique
        // locator match before it renders anything.
        pins: this.annotationPins.map((pin) => ({ ...pin, documentID: session.generation })),
      })
      return true
    } catch {
      this.handlePortFailure(port)
      return false
    }
  }

  destroy(): void {
    ++this.serial
    window.removeEventListener('message', this.handleWindowMessage)
    this.closeSession()
  }

  private readonly handleWindowMessage = (event: MessageEvent): void => {
    const transferredPorts = Array.from(event.ports ?? [])
    const transferredPort = transferredPorts.length === 1 ? transferredPorts[0] : null
    const closeTransferredPort = () => {
      for (const candidate of transferredPorts) {
        try { candidate.close() } catch {}
      }
    }
    const frame = this.getFrame()
    if (!this.project || !frame?.contentWindow) {
      closeTransferredPort()
      return
    }
    if (event.source !== frame.contentWindow || event.origin !== this.expectedOrigin) {
      closeTransferredPort()
      return
    }
    if (!isBridgeReadyMessage(event.data)) {
      closeTransferredPort()
      return
    }
    const generation = typeof event.data.documentID === 'string' ? event.data.documentID.trim() : ''
    if (!generation || generation.length > 128 || !transferredPort) {
      closeTransferredPort()
      return
    }
    if (generation === this.readyGeneration) {
      // READY is idempotent for a document, but every READY carries a fresh
      // handshake endpoint. Never leave a replayed endpoint open.
      closeTransferredPort()
      return
    }
    if (this.started || this.readyGeneration || this.session) {
      // A hot reload replaces the bridge and therefore its document
      // generation. Drop the old port before binding the new document.
      // Desired pins survive, but are rebound only once the new document
      // connects and remain pagePath/locator gated.
      ++this.serial
      this.closeSession()
      this.onState('connecting')
    }
    this.readyGeneration = generation
    this.started = true
    this.expectedOrigin = event.origin
    this.bindBridge(generation, this.serial, transferredPort)
  }

  private bindBridge(generation: string, serial: number, port: MessagePort): void {
    const frame = this.getFrame()
    if (!frame?.contentWindow) {
      try { port.close() } catch {}
      return
    }
    const session: PreviewBridgeSession = { sessionID: previewBridgeRandomID(), generation }
    this.session = session
    this.port?.close()
    this.port = port
    this.port.onmessage = this.handlePortMessage
    try {
      this.port.start()
      if (this.connectionTimer !== undefined) {
        window.clearTimeout(this.connectionTimer)
        this.connectionTimer = undefined
      }
      this.connectionTimer = window.setTimeout(() => {
        this.connectionTimer = undefined
        if (serial !== this.serial || this.session?.sessionID !== session.sessionID) return
        this.onState('unavailable')
        this.closeSession()
      }, 3_000)
      // START travels only on the bridge-created MessagePort, never in a
      // window message visible to same-document app scripts.
      this.port.postMessage({
        type: 'railgrid.preview-bridge.start',
        version: PREVIEW_BRIDGE_PROTOCOL_VERSION,
        sessionID: session.sessionID,
        generation: session.generation,
      })
    } catch {
      this.handlePortFailure(port)
    }
  }

  private readonly handlePortMessage = (event: MessageEvent): void => {
    const session = this.session
    if (!session || !isBridgePortMessage(event.data)) return
    const message = event.data
    if (
      message.version !== PREVIEW_BRIDGE_PROTOCOL_VERSION ||
      message.sessionID !== session.sessionID ||
      message.generation !== session.generation
    ) return

    if (message.type === 'railgrid.preview-bridge.connected') {
      if (this.connectionTimer !== undefined) {
        window.clearTimeout(this.connectionTimer)
        this.connectionTimer = undefined
      }
      const pagePath = previewBridgePagePath(message.path)
      if (!pagePath) {
        this.handlePortFailure(this.port!)
        return
      }
      this.bridgeConnected = true
      this.onDocument?.(session.generation, pagePath)
      if (!this.postAnnotationPins() && !this.port) return
      this.onState('connected')
      return
    }
    if (message.type === PREVIEW_BRIDGE_ANNOTATION_MODE) {
      this.annotationMode = message.active === true
      this.onAnnotationMode?.(this.annotationMode)
      return
    }
    if (message.type === PREVIEW_BRIDGE_ANNOTATION_CANCELLED) {
      this.annotationMode = false
      this.onAnnotationMode?.(false)
      return
    }
    if (message.type === PREVIEW_BRIDGE_ANNOTATION_SELECTED) {
      const selection = previewBridgeAnnotationSelection(message, session.generation)
      if (selection) this.onAnnotation?.(selection)
      return
    }
    if (message.type === PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER) {
      const hover = previewBridgeAnnotationPinHover(message, session.generation)
      if (hover) this.onAnnotationPinHover?.(hover)
      return
    }
    if (message.type === PREVIEW_BRIDGE_ANNOTATION_PIN_SELECTED) {
      const selection = previewBridgeAnnotationPinSelection(message, session.generation)
      if (selection) this.onAnnotationPinSelect?.(selection)
      return
    }
    if (message.type === PREVIEW_BRIDGE_ANNOTATION_PINS_RENDERED) {
      const documentID = typeof message.documentID === 'string' ? message.documentID.slice(0, 128) : ''
      const pagePath = previewBridgePagePath(message.path)
      if (documentID !== session.generation || !pagePath || !Array.isArray(message.pins)) return
      if (message.pins.length > PREVIEW_BRIDGE_MAX_ANNOTATION_PINS) return
      const states: PreviewBridgeAnnotationPinRenderState[] = []
      for (const value of message.pins) {
        if (!value || typeof value !== 'object') continue
        const id = typeof (value as { id?: unknown }).id === 'string' ? (value as { id: string }).id.trim().slice(0, 96) : ''
        if (!id) continue
        states.push({ id, resolved: (value as { resolved?: unknown }).resolved === true })
      }
      this.onAnnotationPinsRendered?.(documentID, pagePath, states)
      return
    }
  }

  private handlePortFailure(port: MessagePort): void {
    if (this.port !== port) return
    try { port.close() } catch {}
    if (this.connectionTimer !== undefined) {
      window.clearTimeout(this.connectionTimer)
      this.connectionTimer = undefined
    }
    this.port = null
    this.bridgeConnected = false
    this.started = false
    this.readyGeneration = ''
    this.annotationMode = false
    this.onAnnotationMode?.(false)
    this.onState('unavailable')
    this.session = null
  }

  private closeSession(): void {
    if (this.annotationMode) this.stopAnnotationMode()
    if (this.connectionTimer !== undefined) {
      window.clearTimeout(this.connectionTimer)
      this.connectionTimer = undefined
    }
    this.port?.close()
    this.port = null
    this.started = false
    this.bridgeConnected = false
    if (this.annotationMode) this.onAnnotationMode?.(false)
    this.annotationMode = false
    this.expectedOrigin = ''
    this.session = null
  }
}

function clonePreviewBridgeAnnotationRect(rect: PreviewBridgeAnnotationRect): PreviewBridgeAnnotationRect {
  return {
    x: rect.x,
    y: rect.y,
    width: rect.width,
    height: rect.height,
  }
}

function previewBridgeRandomID(): string {
  try {
    if (typeof crypto?.randomUUID === 'function') return crypto.randomUUID()
  } catch {}
  const values = new Uint8Array(16)
  crypto.getRandomValues(values)
  values[6] = (values[6] & 0x0f) | 0x40
  values[8] = (values[8] & 0x3f) | 0x80
  const hex = Array.from(values, (value) => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function previewBridgeAnnotationPinsEqual(left: PreviewBridgeAnnotationPin[], right: PreviewBridgeAnnotationPin[]): boolean {
  if (left.length !== right.length) return false
  return left.every((pin, index) => JSON.stringify(pin) === JSON.stringify(right[index]))
}

function clonePreviewBridgeAnnotationTarget(target: PreviewBridgeAnnotationTarget): PreviewBridgeAnnotationTarget {
  const clone: PreviewBridgeAnnotationTarget = {}
  for (const key of ['tag', 'role', 'name', 'text', 'locator', 'locatorStrategy'] as const) {
    if (typeof target[key] === 'string') clone[key] = target[key]
  }
  if (target.rect) clone.rect = clonePreviewBridgeAnnotationRect(target.rect)
  if (Array.isArray(target.ancestors)) clone.ancestors = [...target.ancestors]
  return clone
}

function isBridgeReadyMessage(value: unknown): value is BridgeReadyMessage {
  if (!value || typeof value !== 'object') return false
  const message = value as Partial<BridgeReadyMessage>
  return message.type === 'railgrid.preview-bridge.ready' && message.version === PREVIEW_BRIDGE_PROTOCOL_VERSION
}

function isBridgePortMessage(value: unknown): value is BridgePortMessage {
  if (!value || typeof value !== 'object') return false
  const message = value as Partial<BridgePortMessage>
  return (
    typeof message.type === 'string' &&
    message.version === PREVIEW_BRIDGE_PROTOCOL_VERSION &&
    typeof message.sessionID === 'string' &&
    typeof message.generation === 'string'
  )
}

function previewBridgePagePath(value: unknown): string {
  if (typeof value !== 'string') return ''
  const pagePath = value.replace(/[\u0000-\u001f\u007f]/g, '').trim().slice(0, 1024)
  if (!pagePath.startsWith('/') || pagePath.startsWith('//') || /[?#\\]/.test(pagePath)) return ''
  return pagePath
}

function previewBridgeAnnotationSelection(value: BridgePortMessage, connectedDocumentID: string): PreviewBridgeAnnotationSelection | null {
  if (value.type !== PREVIEW_BRIDGE_ANNOTATION_SELECTED || !value.target || typeof value.target !== 'object') return null
  const documentID = typeof value.documentID === 'string' ? value.documentID.slice(0, 128) : ''
  if (!documentID || documentID !== connectedDocumentID) return null
  const raw = value.target as Record<string, unknown>
  const target = previewBridgeAnnotationTarget(raw)
  if (!target) return null
  const anchor = previewBridgeAnnotationAnchor(value.anchor)
  if (value.anchor !== undefined && !anchor) return null
  const viewport = value.viewport
  if (!viewport || typeof viewport !== 'object') return null
  const rawViewport = viewport as Record<string, unknown>
  const width = typeof rawViewport.width === 'number' && Number.isFinite(rawViewport.width) ? rawViewport.width : null
  const height = typeof rawViewport.height === 'number' && Number.isFinite(rawViewport.height) ? rawViewport.height : null
  const pagePath = previewBridgePagePath(value.path)
  if (width === null || height === null || width <= 0 || height <= 0 || !pagePath) return null
  return {
    documentID,
    pagePath,
    viewport: { width, height },
    target,
    ...(anchor ? { anchor } : {}),
  }
}

function previewBridgeAnnotationAnchor(value: unknown): { x: number; y: number } | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const x = typeof raw.x === 'number' && Number.isFinite(raw.x) && raw.x >= 0 && raw.x <= 1 ? raw.x : null
  const y = typeof raw.y === 'number' && Number.isFinite(raw.y) && raw.y >= 0 && raw.y <= 1 ? raw.y : null
  return x === null || y === null ? null : { x, y }
}

function previewBridgeAnnotationPinHover(value: BridgePortMessage, connectedDocumentID: string): PreviewBridgeAnnotationPinHover | null {
  if (value.type !== PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER || typeof value.id !== 'string' || typeof value.active !== 'boolean') return null
  const documentID = typeof value.documentID === 'string' ? value.documentID.slice(0, 128) : ''
  if (!documentID || documentID !== connectedDocumentID) return null
  const id = value.id.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, 96)
  if (!id) return null
  const pagePath = previewBridgePagePath(value.path)
  if (!pagePath) return null
  const rect = previewBridgeAnnotationPinHoverRect(value.rect)
  if (!rect) return null
  return { id, active: value.active, pagePath, rect }
}

function previewBridgeAnnotationPinSelection(value: BridgePortMessage, connectedDocumentID: string): PreviewBridgeAnnotationPinSelection | null {
  if (value.type !== PREVIEW_BRIDGE_ANNOTATION_PIN_SELECTED || typeof value.id !== 'string') return null
  const documentID = typeof value.documentID === 'string' ? value.documentID.slice(0, 128) : ''
  if (!documentID || documentID !== connectedDocumentID) return null
  const id = value.id.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, 96)
  const pagePath = previewBridgePagePath(value.path)
  const rect = previewBridgeAnnotationPinHoverRect(value.rect)
  const viewport = previewBridgeAnnotationViewport(value.viewport)
  if (!id || !pagePath || !rect || !viewport) return null
  return { id, pagePath, rect, viewport }
}

function previewBridgeAnnotationViewport(value: unknown): { width: number; height: number } | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const width = typeof raw.width === 'number' && Number.isFinite(raw.width) && raw.width > 0 ? raw.width : null
  const height = typeof raw.height === 'number' && Number.isFinite(raw.height) && raw.height > 0 ? raw.height : null
  return width === null || height === null ? null : { width, height }
}

function previewBridgeAnnotationPinHoverRect(value: unknown): PreviewBridgeAnnotationRect | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const number = (candidate: unknown) => typeof candidate === 'number' && Number.isFinite(candidate) ? candidate : null
  const x = number(raw.x)
  const y = number(raw.y)
  const width = number(raw.width)
  const height = number(raw.height)
  if (x === null || y === null || width === null || height === null || width < 0 || height < 0) return null
  return {
    x: Math.max(-100_000, Math.min(100_000, x)),
    y: Math.max(-100_000, Math.min(100_000, y)),
    width: Math.max(0, Math.min(100_000, width)),
    height: Math.max(0, Math.min(100_000, height)),
  }
}

function previewBridgeAnnotationTarget(value: Record<string, unknown>): PreviewBridgeAnnotationTarget | null {
  const tag = typeof value.tag === 'string' ? value.tag.trim().slice(0, 64) : ''
  const rect = value.rect
  if (!rect || typeof rect !== 'object') return null
  const rawRect = rect as Record<string, unknown>
  const number = (candidate: unknown, max = 100_000) => typeof candidate === 'number' && Number.isFinite(candidate) && Math.abs(candidate) <= max ? candidate : null
  const boundedString = (candidate: unknown, max = 240) => typeof candidate === 'string' ? candidate.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : ''
  const boundingRect = {
    x: number(rawRect.x), y: number(rawRect.y), width: number(rawRect.width), height: number(rawRect.height),
  }
  if (Object.values(boundingRect).some((candidate) => candidate === null)) return null
  const target: PreviewBridgeAnnotationTarget = {
    tag,
    rect: boundingRect as PreviewBridgeAnnotationRect,
  }
  for (const key of ['role', 'name', 'text', 'locator', 'locatorStrategy'] as const) {
    const candidate = boundedString(value[key])
    if (candidate) target[key] = candidate
  }
  if (Array.isArray(value.ancestors)) target.ancestors = value.ancestors.slice(0, 16).map((ancestor) => boundedString(ancestor, 256)).filter(Boolean)
  return target
}
