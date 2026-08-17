/*
 * Copyright 2026 The Faros Authors.
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

export const PREVIEW_CONSOLE_PROTOCOL_VERSION = 1

export const PREVIEW_CONSOLE_ANNOTATION_START = 'faros.preview-console.annotation.start'
export const PREVIEW_CONSOLE_ANNOTATION_STOP = 'faros.preview-console.annotation.stop'
export const PREVIEW_CONSOLE_ANNOTATION_PINS = 'faros.preview-console.annotation.pins'
export const PREVIEW_CONSOLE_ANNOTATION_PINS_RENDERED = 'faros.preview-console.annotation.pins-rendered'
export const PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER = 'faros.preview-console.annotation.pin-hover'
export const PREVIEW_CONSOLE_ANNOTATION_PIN_SELECTED = 'faros.preview-console.annotation.pin-selected'
export const PREVIEW_CONSOLE_ANNOTATION_SELECTED = 'faros.preview-console.annotation.selected'
export const PREVIEW_CONSOLE_ANNOTATION_CANCELLED = 'faros.preview-console.annotation.cancelled'
export const PREVIEW_CONSOLE_ANNOTATION_MODE = 'faros.preview-console.annotation.mode'
export const PREVIEW_CONSOLE_MAX_ANNOTATION_PINS = 64

export type PreviewConsoleConnectionState =
  | 'disabled'
  | 'connecting'
  | 'connected'
  | 'unavailable'

export interface PreviewConsoleSession {
  status: 'available' | 'unsupported'
  sessionID: string
  generation: string
  capability: string
  previewOrigin: string
  portalOrigin: string
  expiresAt: string
}

export interface PreviewConsoleEvent {
  sequence: number
  documentID: string
  level: 'debug' | 'info' | 'log' | 'warn' | 'error' | 'pageerror' | 'unhandledrejection'
  message: string
  stack?: string
  sourceURL?: string
  clientTime?: string
}

export interface PreviewConsoleAnnotationRect {
  x: number
  y: number
  width: number
  height: number
}

export interface PreviewConsoleAnnotationTarget {
  tag?: string
  role?: string
  name?: string
  text?: string
  locator?: string
  locatorStrategy?: string
  ancestors?: string[]
  rect?: PreviewConsoleAnnotationRect
}

export interface PreviewConsoleAnnotationSelection {
  documentID: string
  pagePath: string
  viewport: { width: number; height: number }
  target: PreviewConsoleAnnotationTarget
  anchor?: { x: number; y: number }
}

export interface PreviewConsoleAnnotationPin {
  id: string
  number: number
  documentID: string
  pagePath: string
  boundingRect: PreviewConsoleAnnotationRect
  target: PreviewConsoleAnnotationTarget
  anchor?: { x: number; y: number }
}

export interface PreviewConsoleAnnotationPinRenderState {
  id: string
  resolved: boolean
}

export interface PreviewConsoleAnnotationPinHover {
  id: string
  active: boolean
  pagePath: string
  rect: PreviewConsoleAnnotationRect
}

export interface PreviewConsoleAnnotationPinSelection {
  id: string
  pagePath: string
  rect: PreviewConsoleAnnotationRect
  viewport: { width: number; height: number }
}

export interface PreviewConsoleAPI {
  createSession(project: string, generation: string, portalInstanceID: string): Promise<PreviewConsoleSession>
  uploadEvents(project: string, sessionID: string, generation: string, events: PreviewConsoleEvent[], droppedCount: number): Promise<void>
  deleteSession(project: string, sessionID: string): Promise<void>
}

interface PreviewConsoleControllerOptions {
  api: PreviewConsoleAPI
  getFrame: () => HTMLIFrameElement | null
  onState: (state: PreviewConsoleConnectionState) => void
  onAnnotation?: (selection: PreviewConsoleAnnotationSelection) => void
  onAnnotationMode?: (active: boolean) => void
  onAnnotationPinsRendered?: (documentID: string, pagePath: string, states: PreviewConsoleAnnotationPinRenderState[]) => void
  onAnnotationPinHover?: (hover: PreviewConsoleAnnotationPinHover) => void
  onAnnotationPinSelect?: (selection: PreviewConsoleAnnotationPinSelection) => void
  onDocument?: (documentID: string, pagePath: string) => void
}

interface BridgeReadyMessage {
  type: 'faros.preview-console.ready'
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
  events?: PreviewConsoleEvent[]
  droppedCount?: number
}

export class PreviewConsoleController {
  private readonly api: PreviewConsoleAPI
  private readonly getFrame: () => HTMLIFrameElement | null
  private readonly onState: (state: PreviewConsoleConnectionState) => void
  private readonly onAnnotation?: (selection: PreviewConsoleAnnotationSelection) => void
  private readonly onAnnotationMode?: (active: boolean) => void
  private readonly onAnnotationPinsRendered?: (documentID: string, pagePath: string, states: PreviewConsoleAnnotationPinRenderState[]) => void
  private readonly onAnnotationPinHover?: (hover: PreviewConsoleAnnotationPinHover) => void
  private readonly onAnnotationPinSelect?: (selection: PreviewConsoleAnnotationPinSelection) => void
  private readonly onDocument?: (documentID: string, pagePath: string) => void
  private project = ''
  private session: PreviewConsoleSession | null = null
  private port: MessagePort | null = null
  private pending: PreviewConsoleEvent[] = []
  private droppedPending = 0
  private retryBatch: PreviewConsoleEvent[] | null = null
  private flushTimer: number | undefined
  private connectionTimer: number | undefined
  private renewalTimer: number | undefined
  private started = false
  private readyGeneration = ''
  private expectedOrigin = ''
  private serial = 0
  private annotationMode = false
  private bridgeConnected = false
  private annotationPins: PreviewConsoleAnnotationPin[] = []
  private readonly portalInstanceID = previewConsolePortalInstanceID()

  constructor(options: PreviewConsoleControllerOptions) {
    this.api = options.api
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
    // Same-project reconnects intentionally leave remote cleanup to the next
    // create call, which atomically replaces only this portal tab's session.
    // Do not block bridge recovery on a best-effort DELETE.
    await this.closeSession(projectChanged)
    if (serial !== this.serial) return
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
        void this.closeSession()
      }, 3_000)
      // Probe after the iframe load so an early bootstrap-ready message cannot
      // be missed. The bridge's document ID is the immutable generation; a new
      // document therefore cannot replay a capability issued to its predecessor.
      this.getFrame()?.contentWindow?.postMessage({
        type: 'faros.preview-console.probe',
        version: PREVIEW_CONSOLE_PROTOCOL_VERSION,
      }, this.expectedOrigin)
    } catch (error) {
      if (serial === this.serial) {
        this.onState((error as { status?: number })?.status === 404 ? 'disabled' : 'unavailable')
      }
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
    const serial = ++this.serial
    await this.closeSession()
    if (serial !== this.serial) return
    this.project = ''
    this.readyGeneration = ''
    this.annotationPins = []
    this.onState('disabled')
  }

  /** Start element-pick mode only after the authenticated bridge is connected. */
  startAnnotationMode(): boolean {
    const session = this.session
    const port = this.port
    if (!session || !port || !this.started || !this.bridgeConnected || !this.expectedOrigin) return false
    try {
      port.postMessage({
        type: PREVIEW_CONSOLE_ANNOTATION_START,
        version: PREVIEW_CONSOLE_PROTOCOL_VERSION,
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
          type: PREVIEW_CONSOLE_ANNOTATION_STOP,
          version: PREVIEW_CONSOLE_PROTOCOL_VERSION,
          sessionID: session.sessionID,
          generation: session.generation,
        })
      } catch {
        // Session cleanup remains authoritative when the port is unavailable.
        this.handlePortFailure(port)
      }
    }
    if (this.annotationMode) this.onAnnotationMode?.(false)
    this.annotationMode = false
  }

  setAnnotationPins(pins: PreviewConsoleAnnotationPin[]): boolean {
    if (!Array.isArray(pins) || pins.length > PREVIEW_CONSOLE_MAX_ANNOTATION_PINS) return false
    const nextPins = pins.map((pin) => ({
      id: pin.id,
      number: pin.number,
      documentID: pin.documentID,
      pagePath: pin.pagePath,
      // Composer parts are Vue-reactive. MessagePort's structured clone
      // rejects Proxy objects, so make the bridge boundary explicitly plain.
      boundingRect: clonePreviewConsoleAnnotationRect(pin.boundingRect),
      target: clonePreviewConsoleAnnotationTarget(pin.target),
      ...(pin.anchor ? { anchor: { x: pin.anchor.x, y: pin.anchor.y } } : {}),
    }))
    if (previewConsoleAnnotationPinsEqual(this.annotationPins, nextPins)) {
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
        type: PREVIEW_CONSOLE_ANNOTATION_PINS,
        version: PREVIEW_CONSOLE_PROTOCOL_VERSION,
        sessionID: session.sessionID,
        generation: session.generation,
        // Pin provenance retains the document that was annotated, while the
        // transport is rebound to the currently authenticated preview
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
    void this.closeSession()
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
      // A hot reload replaces the bridge and therefore its authenticated
      // document generation. Drop the old port before authorizing the new
      // document. Desired pins survive, but are rebound only after the new
      // capability is authenticated and remain pagePath/locator gated.
      const project = this.project
      const serial = ++this.serial
      this.readyGeneration = generation
      this.onState('connecting')
      void this.closeSession(false).then(() => {
        if (serial !== this.serial || this.project !== project) return
        this.started = true
        this.expectedOrigin = event.origin
        void this.authorizeBridge(project, generation, event.origin, serial, transferredPort)
      })
      return
    }
    this.readyGeneration = generation
    this.started = true
    void this.authorizeBridge(this.project, generation, event.origin, this.serial, transferredPort)
  }

  private async authorizeBridge(project: string, generation: string, origin: string, serial: number, port: MessagePort): Promise<void> {
    let session: PreviewConsoleSession
    try {
      session = await this.api.createSession(project, generation, this.portalInstanceID)
    } catch (error) {
      try { port.close() } catch {}
      if (serial === this.serial) {
        this.readyGeneration = ''
        this.onState((error as { status?: number })?.status === 404 ? 'disabled' : 'unavailable')
        void this.closeSession()
      }
      return
    }
    if (serial !== this.serial || this.project !== project) {
      try { port.close() } catch {}
      if (session.sessionID) void this.api.deleteSession(project, session.sessionID)
      return
    }
    if (session.status !== 'available') {
      try { port.close() } catch {}
      this.readyGeneration = ''
      this.onState('disabled')
      void this.closeSession()
      return
    }
    let scopeMatches = false
    try {
      scopeMatches =
        session.generation === generation &&
        session.previewOrigin === origin &&
        session.portalOrigin === window.location.origin &&
        new URL(session.previewOrigin).origin === session.previewOrigin
    } catch {
      scopeMatches = false
    }
    if (!scopeMatches) {
      try { port.close() } catch {}
      this.readyGeneration = ''
      this.onState('unavailable')
      void this.api.deleteSession(project, session.sessionID).catch(() => {})
      return
    }
    this.session = session
    this.expectedOrigin = origin
    const expiresAt = Date.parse(session.expiresAt)
    if (Number.isFinite(expiresAt)) {
      this.renewalTimer = window.setTimeout(() => {
        this.renewalTimer = undefined
        if (this.session?.sessionID === session.sessionID && this.project === project) {
          void this.reconnect()
        }
      }, Math.max(1_000, expiresAt - Date.now() - 30_000))
    }
    if (!this.getFrame()?.contentWindow) {
      try { port.close() } catch {}
      return
    }
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
        void this.closeSession()
      }, 3_000)
      // The capability stays on the bridge-created MessagePort. It is never
      // included in a window message visible to same-document app scripts.
      this.port.postMessage({
        type: 'faros.preview-console.start',
        version: PREVIEW_CONSOLE_PROTOCOL_VERSION,
        sessionID: session.sessionID,
        generation: session.generation,
        capability: session.capability,
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
      message.version !== PREVIEW_CONSOLE_PROTOCOL_VERSION ||
      message.sessionID !== session.sessionID ||
      message.generation !== session.generation
    ) return

    if (message.type === 'faros.preview-console.connected') {
      if (this.connectionTimer !== undefined) {
        window.clearTimeout(this.connectionTimer)
        this.connectionTimer = undefined
      }
      const pagePath = previewConsolePagePath(message.path)
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
    if (message.type === PREVIEW_CONSOLE_ANNOTATION_MODE) {
      this.annotationMode = message.active === true
      this.onAnnotationMode?.(this.annotationMode)
      return
    }
    if (message.type === PREVIEW_CONSOLE_ANNOTATION_CANCELLED) {
      this.annotationMode = false
      this.onAnnotationMode?.(false)
      return
    }
    if (message.type === PREVIEW_CONSOLE_ANNOTATION_SELECTED) {
      const selection = previewConsoleAnnotationSelection(message, session.generation)
      if (selection) this.onAnnotation?.(selection)
      return
    }
    if (message.type === PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER) {
      const hover = previewConsoleAnnotationPinHover(message, session.generation)
      if (hover) this.onAnnotationPinHover?.(hover)
      return
    }
    if (message.type === PREVIEW_CONSOLE_ANNOTATION_PIN_SELECTED) {
      const selection = previewConsoleAnnotationPinSelection(message, session.generation)
      if (selection) this.onAnnotationPinSelect?.(selection)
      return
    }
    if (message.type === PREVIEW_CONSOLE_ANNOTATION_PINS_RENDERED) {
      const documentID = typeof message.documentID === 'string' ? message.documentID.slice(0, 128) : ''
      const pagePath = previewConsolePagePath(message.path)
      if (documentID !== session.generation || !pagePath || !Array.isArray(message.pins)) return
      if (message.pins.length > PREVIEW_CONSOLE_MAX_ANNOTATION_PINS) return
      const states: PreviewConsoleAnnotationPinRenderState[] = []
      for (const value of message.pins) {
        if (!value || typeof value !== 'object') continue
        const id = typeof (value as { id?: unknown }).id === 'string' ? (value as { id: string }).id.trim().slice(0, 96) : ''
        if (!id) continue
        states.push({ id, resolved: (value as { resolved?: unknown }).resolved === true })
      }
      this.onAnnotationPinsRendered?.(documentID, pagePath, states)
      return
    }
    if (message.type !== 'faros.preview-console.events' || !Array.isArray(message.events)) return
    if (Number.isSafeInteger(message.droppedCount) && Number(message.droppedCount) > 0) {
      this.addDropped(Number(message.droppedCount))
    }
    for (const entry of message.events.slice(0, 32)) {
      if (!isPreviewConsoleEvent(entry) || previewConsoleJSONBytes(entry) > 2_048) {
        this.addDropped(1)
        continue
      }
      this.enqueue(entry)
    }
    if (message.events.length > 32) this.addDropped(message.events.length - 32)
    if (this.pending.length === 0 && this.droppedPending > 0) this.scheduleFlush()
  }

  private handlePortFailure(port: MessagePort): void {
    if (this.port !== port) return
    try { port.close() } catch {}
    if (this.connectionTimer !== undefined) {
      window.clearTimeout(this.connectionTimer)
      this.connectionTimer = undefined
    }
    if (this.renewalTimer !== undefined) {
      window.clearTimeout(this.renewalTimer)
      this.renewalTimer = undefined
    }
    this.port = null
    this.bridgeConnected = false
    this.started = false
    this.readyGeneration = ''
    this.pending = []
    this.droppedPending = 0
    this.retryBatch = null
    this.annotationMode = false
    this.onAnnotationMode?.(false)
    this.onState('unavailable')
    const session = this.session
    const project = this.project
    this.session = null
    if (session?.sessionID && project) void this.api.deleteSession(project, session.sessionID).catch(() => {})
  }

  private enqueue(event: PreviewConsoleEvent): void {
    // The backend remains the security boundary and re-sanitizes every event.
    // This client bound prevents a noisy app from growing portal memory.
    if (this.pending.length >= 256) {
      this.pending.shift()
      this.addDropped(1)
    }
    this.pending.push(event)
    if (this.pending.length >= 16) {
      void this.flush()
      return
    }
    this.scheduleFlush()
  }

  private addDropped(count: number): void {
    if (!Number.isFinite(count) || count <= 0) return
    this.droppedPending = Math.min(1_000_000, this.droppedPending + Math.floor(count))
  }

  private scheduleFlush(): void {
    if (this.flushTimer === undefined) {
      this.flushTimer = window.setTimeout(() => {
        this.flushTimer = undefined
        void this.flush()
      }, 500)
    }
  }

  private async flush(): Promise<void> {
    const session = this.session
    const project = this.project
    if (!session || !project || this.retryBatch) return
    if (this.flushTimer !== undefined) {
      window.clearTimeout(this.flushTimer)
      this.flushTimer = undefined
    }
    const batch = takePreviewConsoleUploadBatch(this.pending, session.generation, this.droppedPending)
    const droppedCount = this.droppedPending
    if (batch.length === 0 && droppedCount === 0) return
    this.droppedPending = 0
    this.retryBatch = batch
    try {
      await this.api.uploadEvents(project, session.sessionID, session.generation, batch, droppedCount)
    } catch {
      // One bounded retry; events are deliberately dropped after it.
      try {
        await this.api.uploadEvents(project, session.sessionID, session.generation, batch, droppedCount)
      } catch (error) {
        // Advisory evidence must never create an unbounded retry queue.
        this.addDropped(batch.length + droppedCount)
        const status = (error as { status?: number })?.status
        if (status === 404 || status === 410) {
          this.onState('unavailable')
          void this.closeSession()
        }
      }
    } finally {
      this.retryBatch = null
      if (this.pending.length > 0) void this.flush()
    }
  }

  private async closeSession(deleteRemote = true): Promise<void> {
    if (this.annotationMode) this.stopAnnotationMode()
    if (this.flushTimer !== undefined) {
      window.clearTimeout(this.flushTimer)
      this.flushTimer = undefined
    }
    if (this.connectionTimer !== undefined) {
      window.clearTimeout(this.connectionTimer)
      this.connectionTimer = undefined
    }
    if (this.renewalTimer !== undefined) {
      window.clearTimeout(this.renewalTimer)
      this.renewalTimer = undefined
    }
    this.pending = []
    this.droppedPending = 0
    this.retryBatch = null
    this.port?.close()
    this.port = null
    this.started = false
    this.bridgeConnected = false
    if (this.annotationMode) this.onAnnotationMode?.(false)
    this.annotationMode = false
    this.expectedOrigin = ''
    const session = this.session
    const project = this.project
    this.session = null
    if (deleteRemote && session?.sessionID && project) {
      try {
        await this.api.deleteSession(project, session.sessionID)
      } catch {
        // Server-side expiry is the cleanup fallback.
      }
    }
  }
}

function clonePreviewConsoleAnnotationRect(rect: PreviewConsoleAnnotationRect): PreviewConsoleAnnotationRect {
  return {
    x: rect.x,
    y: rect.y,
    width: rect.width,
    height: rect.height,
  }
}

function previewConsolePortalInstanceID(): string {
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

function previewConsoleAnnotationPinsEqual(left: PreviewConsoleAnnotationPin[], right: PreviewConsoleAnnotationPin[]): boolean {
  if (left.length !== right.length) return false
  return left.every((pin, index) => JSON.stringify(pin) === JSON.stringify(right[index]))
}

function clonePreviewConsoleAnnotationTarget(target: PreviewConsoleAnnotationTarget): PreviewConsoleAnnotationTarget {
  const clone: PreviewConsoleAnnotationTarget = {}
  for (const key of ['tag', 'role', 'name', 'text', 'locator', 'locatorStrategy'] as const) {
    if (typeof target[key] === 'string') clone[key] = target[key]
  }
  if (target.rect) clone.rect = clonePreviewConsoleAnnotationRect(target.rect)
  if (Array.isArray(target.ancestors)) clone.ancestors = [...target.ancestors]
  return clone
}

function isBridgeReadyMessage(value: unknown): value is BridgeReadyMessage {
  if (!value || typeof value !== 'object') return false
  const message = value as Partial<BridgeReadyMessage>
  return message.type === 'faros.preview-console.ready' && message.version === PREVIEW_CONSOLE_PROTOCOL_VERSION
}

function isBridgePortMessage(value: unknown): value is BridgePortMessage {
  if (!value || typeof value !== 'object') return false
  const message = value as Partial<BridgePortMessage>
  return (
    typeof message.type === 'string' &&
    message.version === PREVIEW_CONSOLE_PROTOCOL_VERSION &&
    typeof message.sessionID === 'string' &&
    typeof message.generation === 'string'
  )
}

function previewConsolePagePath(value: unknown): string {
  if (typeof value !== 'string') return ''
  const pagePath = value.replace(/[\u0000-\u001f\u007f]/g, '').trim().slice(0, 1024)
  if (!pagePath.startsWith('/') || pagePath.startsWith('//') || /[?#\\]/.test(pagePath)) return ''
  return pagePath
}

function previewConsoleAnnotationSelection(value: BridgePortMessage, authenticatedDocumentID: string): PreviewConsoleAnnotationSelection | null {
  if (value.type !== PREVIEW_CONSOLE_ANNOTATION_SELECTED || !value.target || typeof value.target !== 'object') return null
  const documentID = typeof value.documentID === 'string' ? value.documentID.slice(0, 128) : ''
  if (!documentID || documentID !== authenticatedDocumentID) return null
  const raw = value.target as Record<string, unknown>
  const target = previewConsoleAnnotationTarget(raw)
  if (!target) return null
  const anchor = previewConsoleAnnotationAnchor(value.anchor)
  if (value.anchor !== undefined && !anchor) return null
  const viewport = value.viewport
  if (!viewport || typeof viewport !== 'object') return null
  const rawViewport = viewport as Record<string, unknown>
  const width = typeof rawViewport.width === 'number' && Number.isFinite(rawViewport.width) ? rawViewport.width : null
  const height = typeof rawViewport.height === 'number' && Number.isFinite(rawViewport.height) ? rawViewport.height : null
  const pagePath = previewConsolePagePath(value.path)
  if (width === null || height === null || width <= 0 || height <= 0 || !pagePath) return null
  return {
    documentID,
    pagePath,
    viewport: { width, height },
    target,
    ...(anchor ? { anchor } : {}),
  }
}

function previewConsoleAnnotationAnchor(value: unknown): { x: number; y: number } | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const x = typeof raw.x === 'number' && Number.isFinite(raw.x) && raw.x >= 0 && raw.x <= 1 ? raw.x : null
  const y = typeof raw.y === 'number' && Number.isFinite(raw.y) && raw.y >= 0 && raw.y <= 1 ? raw.y : null
  return x === null || y === null ? null : { x, y }
}

function previewConsoleAnnotationPinHover(value: BridgePortMessage, authenticatedDocumentID: string): PreviewConsoleAnnotationPinHover | null {
  if (value.type !== PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER || typeof value.id !== 'string' || typeof value.active !== 'boolean') return null
  const documentID = typeof value.documentID === 'string' ? value.documentID.slice(0, 128) : ''
  if (!documentID || documentID !== authenticatedDocumentID) return null
  const id = value.id.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, 96)
  if (!id) return null
  const pagePath = previewConsolePagePath(value.path)
  if (!pagePath) return null
  const rect = previewConsoleAnnotationPinHoverRect(value.rect)
  if (!rect) return null
  return { id, active: value.active, pagePath, rect }
}

function previewConsoleAnnotationPinSelection(value: BridgePortMessage, authenticatedDocumentID: string): PreviewConsoleAnnotationPinSelection | null {
  if (value.type !== PREVIEW_CONSOLE_ANNOTATION_PIN_SELECTED || typeof value.id !== 'string') return null
  const documentID = typeof value.documentID === 'string' ? value.documentID.slice(0, 128) : ''
  if (!documentID || documentID !== authenticatedDocumentID) return null
  const id = value.id.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, 96)
  const pagePath = previewConsolePagePath(value.path)
  const rect = previewConsoleAnnotationPinHoverRect(value.rect)
  const viewport = previewConsoleAnnotationViewport(value.viewport)
  if (!id || !pagePath || !rect || !viewport) return null
  return { id, pagePath, rect, viewport }
}

function previewConsoleAnnotationViewport(value: unknown): { width: number; height: number } | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const width = typeof raw.width === 'number' && Number.isFinite(raw.width) && raw.width > 0 ? raw.width : null
  const height = typeof raw.height === 'number' && Number.isFinite(raw.height) && raw.height > 0 ? raw.height : null
  return width === null || height === null ? null : { width, height }
}

function previewConsoleAnnotationPinHoverRect(value: unknown): PreviewConsoleAnnotationRect | null {
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

function previewConsoleAnnotationTarget(value: Record<string, unknown>): PreviewConsoleAnnotationTarget | null {
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
  const target: PreviewConsoleAnnotationTarget = {
    tag,
    rect: boundingRect as PreviewConsoleAnnotationRect,
  }
  for (const key of ['role', 'name', 'text', 'locator', 'locatorStrategy'] as const) {
    const candidate = boundedString(value[key])
    if (candidate) target[key] = candidate
  }
  if (Array.isArray(value.ancestors)) target.ancestors = value.ancestors.slice(0, 16).map((ancestor) => boundedString(ancestor, 256)).filter(Boolean)
  return target
}

function isPreviewConsoleEvent(value: unknown): value is PreviewConsoleEvent {
  if (!value || typeof value !== 'object') return false
  const event = value as Partial<PreviewConsoleEvent>
  return (
    typeof event.level === 'string' &&
    ['debug', 'info', 'log', 'warn', 'error', 'pageerror', 'unhandledrejection'].includes(event.level) &&
    Number.isSafeInteger(event.sequence) &&
    Number(event.sequence) > 0 &&
    typeof event.documentID === 'string' &&
    typeof event.message === 'string'
  )
}

function previewConsoleJSONBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength
  } catch {
    return Number.POSITIVE_INFINITY
  }
}

export function takePreviewConsoleUploadBatch(
  pending: PreviewConsoleEvent[],
  generation: string,
  droppedCount: number,
  maxBytes = 28 << 10,
): PreviewConsoleEvent[] {
  const batch: PreviewConsoleEvent[] = []
  while (pending.length > 0 && batch.length < 16) {
    const candidate = pending[0]
    const next = [...batch, candidate]
    const body = {
      generation,
      protocolVersion: PREVIEW_CONSOLE_PROTOCOL_VERSION,
      droppedCount,
      events: next,
    }
    if (previewConsoleJSONBytes(body) > maxBytes) break
    batch.push(candidate)
    pending.shift()
  }
  return batch
}
