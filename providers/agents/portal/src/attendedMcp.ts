import { ApiError } from './api-error'
import { presentRequestFailure } from './request-errors'
import {
  MAX_AGENT_ARTIFACT_DATA_BYTES,
  MAX_AGENT_ARTIFACT_HTML_BYTES,
  MAX_AGENT_ARTIFACT_INPUT_BYTES,
  MAX_AGENT_ARTIFACT_SOURCES,
  MAX_AGENT_ARTIFACT_ACTIONS,
} from './agentArtifacts'

export interface SSEEvent<T = unknown> {
  id?: string
  event: string
  data: T
}

// Bound the response from known parser maxima. Non-HTML artifact fields can
// appear once in structuredContent and again as JSON inside content[].text.
// Six bytes per UTF-16 code unit covers Go's HTML-safe / JSON control escaping;
// the nested text copy can add one more backslash byte per escaped unit. App
// HTML appears only once in _meta, so it uses the six-byte bound alone.
const MAX_SOURCE_CODE_UNITS = MAX_AGENT_ARTIFACT_SOURCES * (
  256 + 253 + 64 + 128 + 253 + 253 + 128 +
  MAX_AGENT_ARTIFACT_ACTIONS * (253 + 64 + 128 + 253 + 253 + 253 + 32)
)
const MAX_ARTIFACT_METADATA_CODE_UNITS = 16 * 1024
const MAX_ATTESTED_NON_HTML_CODE_UNITS = MAX_AGENT_ARTIFACT_DATA_BYTES +
  MAX_AGENT_ARTIFACT_INPUT_BYTES + MAX_SOURCE_CODE_UNITS + MAX_ARTIFACT_METADATA_CODE_UNITS
const MAX_JSON_ESCAPE_BYTES_PER_CODE_UNIT = 6
const MAX_NESTED_JSON_ESCAPE_BYTES_PER_CODE_UNIT = 7
const MAX_MCP_ENVELOPE_BYTES = 256 * 1024
export const MAX_ATTENDED_MCP_RESPONSE_BYTES =
  MAX_JSON_ESCAPE_BYTES_PER_CODE_UNIT * MAX_AGENT_ARTIFACT_HTML_BYTES +
  (MAX_JSON_ESCAPE_BYTES_PER_CODE_UNIT + MAX_NESTED_JSON_ESCAPE_BYTES_PER_CODE_UNIT) * MAX_ATTESTED_NON_HTML_CODE_UNITS +
  MAX_MCP_ENVELOPE_BYTES
export const ATTENDED_MCP_TIMEOUT_MS = 30_000

type AttendedRPCMessage = {
  id?: unknown
  jsonrpc?: unknown
  error?: unknown
  result?: {
    isError?: unknown
    content?: unknown
    structuredContent?: unknown
    _meta?: unknown
  }
}

export interface AttendedMcpCallOptions {
  fetcher: typeof fetch
  url: string
  headers: HeadersInit
  requestID: number
  name: string
  args: Record<string, unknown>
  signal?: AbortSignal
  timeoutMs?: number
}

export interface AttendedMcpCallResult<T> {
  output: T
  metadata?: unknown
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function invalidResponse(): ApiError {
  return new ApiError(502, 'The Agents service returned an invalid response.')
}

function parseRPCMessage(value: unknown, expectedID: number): AttendedRPCMessage {
  if (!isRecord(value) || value.jsonrpc !== '2.0' || value.id !== expectedID) throw invalidResponse()
  const message = value as AttendedRPCMessage
  const hasResult = Object.prototype.hasOwnProperty.call(value, 'result')
  const hasError = Object.prototype.hasOwnProperty.call(value, 'error')
  if (hasResult === hasError || (hasResult && !isRecord(message.result)) || (hasError && !isRecord(message.error))) throw invalidResponse()
  return message
}

function responseError(value: unknown, fallbackStatus: number, fallbackReason: string, fallbackDiagnostic: string): ApiError {
  const outer = isRecord(value) ? value : {}
  const nested = isRecord(outer.error) ? outer.error : outer
  const status = typeof nested.httpStatus === 'number' && Number.isSafeInteger(nested.httpStatus) && nested.httpStatus >= 400 && nested.httpStatus <= 599
    ? nested.httpStatus : fallbackStatus
  const reason = typeof nested.reason === 'string' && nested.reason ? nested.reason : fallbackReason
  const diagnostic = typeof nested.message === 'string' && nested.message ? nested.message
    : typeof outer.message === 'string' && outer.message ? outer.message : fallbackDiagnostic
  const failure = presentRequestFailure(status, diagnostic, reason)
  return new ApiError(status, failure.message, failure.technicalDiagnostic, reason)
}

function waitForAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(new DOMException('The attended tool request was aborted.', 'AbortError'))
  return new Promise((resolve, reject) => {
    const cleanup = () => signal.removeEventListener('abort', onAbort)
    const onAbort = () => { cleanup(); reject(new DOMException('The attended tool request was aborted.', 'AbortError')) }
    signal.addEventListener('abort', onAbort, { once: true })
    if (signal.aborted) { onAbort(); return }
    promise.then(value => {
      cleanup()
      if (signal.aborted) {
        const response = value as unknown as Response
        if (response?.body) void response.body.cancel().catch(() => undefined)
        reject(new DOMException('The attended tool request was aborted.', 'AbortError'))
      } else resolve(value)
    }, error => { cleanup(); reject(error) })
  })
}

async function readChunk(reader: ReadableStreamDefaultReader<Uint8Array>, signal?: AbortSignal): Promise<ReadableStreamReadResult<Uint8Array>> {
  if (!signal) return reader.read()
  if (signal.aborted) throw new DOMException('The attended tool request was aborted.', 'AbortError')
  return waitForAbort(reader.read(), signal)
}

function checkDeclaredLength(response: Response, limit: number): void {
  const declaredLength = Number(response.headers.get('content-length'))
  if (Number.isFinite(declaredLength) && declaredLength > limit) throw new ApiError(502, 'The Agents service returned an oversized response.')
}

async function readBoundedText(response: Response, limit: number, signal: AbortSignal): Promise<string> {
  const reader = response.body?.getReader()
  if (!reader) throw invalidResponse()
  try {
    checkDeclaredLength(response, limit)
    const decoder = new TextDecoder()
    let text = ''
    let size = 0
    for (;;) {
      const { done, value } = await readChunk(reader, signal)
      if (done) break
      size += value.byteLength
      if (size > limit) throw new ApiError(502, 'The Agents service returned an oversized response.')
      text += decoder.decode(value, { stream: true })
    }
    return text + decoder.decode()
  } finally {
    try { void reader.cancel().catch(() => undefined) } catch { /* stream may already be closed */ }
    try { reader.releaseLock() } catch { /* pending reads release the lock on completion */ }
  }
}

async function httpFailure(response: Response, signal: AbortSignal): Promise<ApiError> {
  let body: unknown
  if (response.body) {
    const text = await readBoundedText(response, MAX_MCP_ENVELOPE_BYTES, signal)
    try { body = JSON.parse(text) as unknown } catch { body = text }
  }
  const record = isRecord(body) ? body : {}
  const nested = isRecord(record.error) ? record.error : record
  const diagnostic = typeof nested.message === 'string' && nested.message ? nested.message
    : typeof record.message === 'string' && record.message ? record.message
      : typeof body === 'string' && body ? body : response.statusText || `HTTP ${response.status}`
  return responseError(body, response.status, typeof nested.reason === 'string' ? nested.reason : '', diagnostic)
}

/** Call an attended MCP tool with a single bounded JSON/SSE and abort path. */
export async function callAttendedMcpTool<T>(options: AttendedMcpCallOptions): Promise<AttendedMcpCallResult<T>> {
  const controller = new AbortController()
  let callerAborted = false
  const onCallerAbort = () => { callerAborted = true; controller.abort() }
  if (options.signal?.aborted) onCallerAbort()
  else options.signal?.addEventListener('abort', onCallerAbort, { once: true })
  const timeout = setTimeout(() => controller.abort(), options.timeoutMs ?? ATTENDED_MCP_TIMEOUT_MS)
  try {
    const response = await waitForAbort(options.fetcher(options.url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { ...Object.fromEntries(new Headers(options.headers).entries()), Accept: 'application/json, text/event-stream' },
      body: JSON.stringify({ jsonrpc: '2.0', id: options.requestID, method: 'tools/call', params: { name: options.name, arguments: options.args } }),
      signal: controller.signal,
    }), controller.signal)
    if (!response.ok) throw await httpFailure(response, controller.signal)

    let raw: unknown
    if ((response.headers.get('content-type') || '').toLowerCase().includes('text/event-stream')) {
      checkDeclaredLength(response, MAX_ATTENDED_MCP_RESPONSE_BYTES)
      for await (const event of readSSE(response.body || new ReadableStream<Uint8Array>(), controller.signal, MAX_ATTENDED_MCP_RESPONSE_BYTES)) {
        if (isRecord(event.data) && event.data.id === options.requestID) { raw = event.data; break }
      }
      if (raw === undefined) throw invalidResponse()
    } else {
      try { raw = JSON.parse(await readBoundedText(response, MAX_ATTENDED_MCP_RESPONSE_BYTES, controller.signal)) as unknown } catch (error) {
        if (error instanceof ApiError) throw error
        throw invalidResponse()
      }
    }

    const message = parseRPCMessage(raw, options.requestID)
    if (message.error !== undefined) {
      const error = isRecord(message.error) ? message.error : {}
      throw responseError(error.data ?? error, 502, 'UpstreamError', typeof error.message === 'string' ? error.message : 'The Agents service could not complete the request.')
    }
    const result = message.result!
    const content = Array.isArray(result.content) ? result.content : []
    const text = content.find(item => isRecord(item) && item.type === 'text' && typeof item.text === 'string')
    const resultText = isRecord(text) && typeof text.text === 'string' ? text.text : undefined
    let output: unknown = result.structuredContent
    if (output === undefined && resultText) {
      try { output = JSON.parse(resultText) as unknown } catch { /* structured result is required below */ }
    }
    if (result.isError === true) throw responseError(output, 403, 'Forbidden', resultText || 'The Agents service refused the request.')
    if (!isRecord(output)) throw new ApiError(502, 'The Agents service returned an invalid result.')
    return { output: output as T, ...(isRecord(result._meta) ? { metadata: result._meta } : {}) }
  } catch (error) {
    if (controller.signal.aborted) {
      if (callerAborted) throw new DOMException('The attended tool request was aborted.', 'AbortError')
      const failure = presentRequestFailure(504, 'The attended Agents request exceeded its 30-second deadline.')
      throw new ApiError(504, failure.message, failure.technicalDiagnostic, 'Timeout')
    }
    throw error
  } finally {
    clearTimeout(timeout)
    options.signal?.removeEventListener('abort', onCallerAbort)
  }
}

// Framing follows the SSE spec and is shared by attended MCP responses and
// chat/event streams so newline and abort handling cannot drift.
export async function* readSSE(body: ReadableStream<Uint8Array>, signal?: AbortSignal, maxBytes = Number.POSITIVE_INFINITY): AsyncGenerator<SSEEvent> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let bytesRead = 0
  try {
    for (;;) {
      const { done, value } = await readChunk(reader, signal)
      if (done) break
      bytesRead += value.byteLength
      if (bytesRead > maxBytes) throw new ApiError(502, 'The Agents service returned an oversized response.')
      buffer += decoder.decode(value, { stream: true })
      buffer = buffer.replace(/\r\n|\r/g, '\n')
      let index: number
      while ((index = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, index)
        buffer = buffer.slice(index + 2)
        const event = parseSSEFrame(frame)
        if (event) yield event
      }
      if (signal?.aborted) break
    }
    buffer += decoder.decode()
  } finally {
    try { void reader.cancel().catch(() => undefined) } catch { /* stream may already be closed */ }
    try { reader.releaseLock() } catch { /* pending reads release the lock on completion */ }
  }
}

export function parseSSEFrame(frame: string): SSEEvent | null {
  let event = 'message'
  let id: string | undefined
  const dataLines: string[] = []
  for (const line of frame.split('\n')) {
    if (!line || line.startsWith(':')) continue
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    else if (field === 'data') dataLines.push(value)
    else if (field === 'id') id = value
  }
  if (!dataLines.length) return null
  const raw = dataLines.join('\n')
  try { return { id, event, data: JSON.parse(raw) } }
  catch { return { id, event, data: raw } }
}
