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

export interface PreviewConsoleAPI {
  createSession(project: string, generation: string): Promise<PreviewConsoleSession>
  uploadEvents(project: string, sessionID: string, generation: string, events: PreviewConsoleEvent[], droppedCount: number): Promise<void>
  deleteSession(project: string, sessionID: string): Promise<void>
}

interface PreviewConsoleControllerOptions {
  api: PreviewConsoleAPI
  getFrame: () => HTMLIFrameElement | null
  onState: (state: PreviewConsoleConnectionState) => void
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
  events?: PreviewConsoleEvent[]
  droppedCount?: number
}

export class PreviewConsoleController {
  private readonly api: PreviewConsoleAPI
  private readonly getFrame: () => HTMLIFrameElement | null
  private readonly onState: (state: PreviewConsoleConnectionState) => void
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
  private expectedOrigin = ''
  private serial = 0

  constructor(options: PreviewConsoleControllerOptions) {
    this.api = options.api
    this.getFrame = options.getFrame
    this.onState = options.onState
    window.addEventListener('message', this.handleWindowMessage)
  }

  async connect(project: string): Promise<void> {
    const serial = ++this.serial
    await this.closeSession()
    if (serial !== this.serial) return
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

  async disconnect(): Promise<void> {
    const serial = ++this.serial
    await this.closeSession()
    if (serial !== this.serial) return
    this.project = ''
    this.onState('disabled')
  }

  destroy(): void {
    ++this.serial
    window.removeEventListener('message', this.handleWindowMessage)
    void this.closeSession()
  }

  private readonly handleWindowMessage = (event: MessageEvent): void => {
    const frame = this.getFrame()
    if (!this.project || !frame?.contentWindow) return
    if (event.source !== frame.contentWindow || event.origin !== this.expectedOrigin) return
    if (!isBridgeReadyMessage(event.data)) return
    if (this.started) return
    const generation = event.data.documentID?.trim()
    if (!generation || generation.length > 128) return
    this.started = true
    void this.authorizeBridge(this.project, generation, event.origin, this.serial)
  }

  private async authorizeBridge(project: string, generation: string, origin: string, serial: number): Promise<void> {
    let session: PreviewConsoleSession
    try {
      session = await this.api.createSession(project, generation)
    } catch (error) {
      if (serial === this.serial) {
        this.onState((error as { status?: number })?.status === 404 ? 'disabled' : 'unavailable')
        void this.closeSession()
      }
      return
    }
    if (serial !== this.serial || this.project !== project) {
      if (session.sessionID) void this.api.deleteSession(project, session.sessionID)
      return
    }
    if (session.status !== 'available') {
      this.onState('unavailable')
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
      this.onState('unavailable')
      void this.api.deleteSession(project, session.sessionID)
      return
    }
    this.session = session
    const expiresAt = Date.parse(session.expiresAt)
    if (Number.isFinite(expiresAt)) {
      this.renewalTimer = window.setTimeout(() => {
        this.renewalTimer = undefined
        if (this.session?.sessionID === session.sessionID && this.project === project) {
          void this.connect(project)
        }
      }, Math.max(1_000, expiresAt - Date.now() - 30_000))
    }
    const frame = this.getFrame()
    if (!frame?.contentWindow) return
    const channel = new MessageChannel()
    this.port?.close()
    this.port = channel.port1
    this.port.onmessage = this.handlePortMessage
    this.port.start()
    frame.contentWindow.postMessage({
      type: 'faros.preview-console.start',
      version: PREVIEW_CONSOLE_PROTOCOL_VERSION,
      sessionID: session.sessionID,
      generation: session.generation,
      capability: session.capability,
    }, session.previewOrigin, [channel.port2])
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
      this.onState('connected')
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

  private async closeSession(): Promise<void> {
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
    this.expectedOrigin = ''
    const session = this.session
    const project = this.project
    this.session = null
    if (session?.sessionID && project) {
      try {
        await this.api.deleteSession(project, session.sessionID)
      } catch {
        // Server-side expiry is the cleanup fallback.
      }
    }
  }
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
