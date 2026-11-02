(() => {
  'use strict'
  const MAX_BODY = 24 * 1024 * 1024
  const MAX_RESPONSE = 32 * 1024 * 1024
  let port
  let nonce
  let hostOrigin
  let context
  let apiGroups = []
  let requestId = 0
  const pending = new Map()
  const encoder = new TextEncoder()

  function send(message, transfer) { if (port) port.postMessage(message, transfer || []) }
  function failure(id, message, name) { send({ type: 'fetch-error', id, message, name }) }
  function normalizeURL(input) {
    const raw = typeof input === 'string' ? input : input && input.url
    return new URL(raw || '', hostOrigin)
  }
  function encodeBody(body) {
    if (body == null) return { type: 'none' }
    if (typeof body === 'string') {
      const bytes = encoder.encode(body)
      if (bytes.byteLength > MAX_BODY) throw new TypeError('Request body exceeds the 24 MiB safety limit.')
      return { type: 'string', value: body }
    }
    if (body instanceof URLSearchParams) return { type: 'string', value: body.toString() }
    if (body instanceof ArrayBuffer) {
      if (body.byteLength > MAX_BODY) throw new TypeError('Request body exceeds the 24 MiB safety limit.')
      return { type: 'bytes', value: body.slice(0) }
    }
    if (ArrayBuffer.isView(body)) return encodeBody(body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength))
    if (body instanceof Blob) {
      if (body.size > MAX_BODY) throw new TypeError('Request body exceeds the 24 MiB safety limit.')
      return body.arrayBuffer().then(bytes => ({ type: 'bytes', value: bytes }))
    }
    if (body instanceof FormData) {
      const entries = []
      let size = 0
      for (const [name, value] of body.entries()) {
        if (typeof value === 'string') { size += encoder.encode(value).byteLength; entries.push([name, value]) }
        else { size += value.size; entries.push([name, value.arrayBuffer().then(bytes => ({ name: value.name, type: value.type, bytes }))]) }
      }
      if (size > MAX_BODY) throw new TypeError('Request body exceeds the 24 MiB safety limit.')
      return Promise.all(entries.map(async ([name, value]) => [name, await value])).then(entries => ({ type: 'form-data', entries }))
    }
    throw new TypeError('Unsupported request body type.')
  }
  function fetchBridge(input, init = {}) {
    const id = String(++requestId)
    const request = input instanceof Request ? input : null
    const method = String(init.method || request?.method || 'GET').toUpperCase()
    const url = normalizeURL(request || input)
    const headers = new Headers(request?.headers)
    new Headers(init.headers).forEach((value, key) => headers.set(key, value))
    const body = init.body !== undefined ? init.body : (request && method !== 'GET' && method !== 'HEAD' ? request.clone().arrayBuffer() : null)
    const encoded = body instanceof Promise ? body.then(encodeBody) : encodeBody(body)
    const signal = init.signal || request?.signal
    const abort = () => {
      send({ type: 'fetch-cancel', id })
      const state = pending.get(id)
      if (!state) return
      pending.delete(id)
      state.signal?.removeEventListener('abort', state.abort)
      const error = new DOMException('The operation was aborted.', 'AbortError')
      if (state.response) { wakePull(state); state.streamController?.error(error) }
      else state.reject(error)
    }
    if (signal?.aborted) return Promise.reject(new DOMException('The operation was aborted.', 'AbortError'))
    return Promise.resolve(encoded).then(encodedBody => new Promise((resolve, reject) => {
      const state = { resolve, reject, response: null, signal, abort, total: 0, queued: null, terminal: null, streamController: null, pullResolve: null, pullController: null }
      pending.set(id, state)
      signal?.addEventListener('abort', abort, { once: true })
      if (signal?.aborted) { abort(); return }
      const transfer = encodedBody.type === 'bytes' ? [encodedBody.value] : []
      send({ type: 'fetch', id, url: url.toString(), method, headers: [...headers.entries()], body: encodedBody }, transfer)
    }))
  }
  fetchBridge.raw = fetchBridge
  function consume(message) {
    if (message.type === 'context') {
      context = message.context
      publishContext()
      return
    }
    const state = pending.get(message.id)
    if (!state) return
    if (message.type === 'fetch-response') {
      state.response = { status: message.status, statusText: message.statusText, headers: message.headers || [] }
      const stream = new ReadableStream({
        start(controller) { state.streamController = controller },
        pull(controller) {
          if (state.queued) {
            deliverChunk(state, message.id, controller)
            if (state.terminal) finishStream(state, message.id, controller)
            return
          }
          if (state.terminal && !state.queued) finishStream(state, message.id, controller)
          else if (!state.queued) return new Promise(resolve => {
            state.pullResolve = resolve
            state.pullController = controller
          })
        },
        cancel(reason) {
          send({ type: 'fetch-cancel', id: message.id })
          pending.delete(message.id)
          state.signal?.removeEventListener('abort', state.abort)
          wakePull(state)
          state.terminal = { type: 'error', error: reason || new DOMException('The response was cancelled.', 'AbortError') }
        },
      }, { highWaterMark: 0 })
      const status = state.response.status || 200
      state.resolve(new Response([204, 205, 304].includes(status) ? null : stream, state.response))
    } else if (message.type === 'fetch-chunk') {
      const isBuffer = Object.prototype.toString.call(message.bytes) === '[object ArrayBuffer]'
      const bytes = isBuffer ? message.bytes : new ArrayBuffer(0)
      state.total += bytes.byteLength
      if (state.total > MAX_RESPONSE || state.queued) {
        const error = new TypeError('Provider response exceeds the 32 MiB safety limit or exceeded stream backpressure.')
        if (state.response) {
          state.terminal = { type: 'error', error }
          if (state.streamController) finishStream(state, message.id, state.streamController)
        } else state.reject(error)
        pending.delete(message.id)
        state.signal?.removeEventListener('abort', state.abort)
        send({ type: 'fetch-cancel', id: message.id })
        return
      }
      state.queued = { bytes, seq: message.seq }
      if (state.pullController) deliverChunk(state, message.id, state.pullController)
    } else if (message.type === 'fetch-end') {
      if (!state.response) {
        pending.delete(message.id)
        state.signal?.removeEventListener('abort', state.abort)
        state.reject(new TypeError('Provider response ended before its headers arrived.'))
      } else {
        state.terminal = { type: 'end' }
        wakePull(state)
        if (!state.queued && state.streamController) finishStream(state, message.id, state.streamController)
      }
    } else if (message.type === 'fetch-error') {
      pending.delete(message.id)
      state.signal?.removeEventListener('abort', state.abort)
      const error = new DOMException(message.message, message.name || 'TypeError')
      if (state.response) {
        state.terminal = { type: 'error', error }
        wakePull(state)
        state.streamController?.error(error)
      }
      else state.reject(error)
    }
  }
  function finishStream(state, id, controller) {
    const terminal = state.terminal
    if (!terminal) return
    state.terminal = null
    wakePull(state)
    if (terminal.type === 'end') controller.close()
    else controller.error(terminal.error)
    pending.delete(id)
    state.signal?.removeEventListener('abort', state.abort)
  }
  function deliverChunk(state, id, controller) {
    const chunk = state.queued
    if (!chunk) return
    state.queued = null
    state.pullController = null
    controller.enqueue(new Uint8Array(chunk.bytes))
    send({ type: 'fetch-ack', id, seq: chunk.seq })
    wakePull(state)
  }
  function wakePull(state) {
    const resolve = state.pullResolve
    state.pullResolve = null
    resolve?.()
  }
  function configure(init) {
    context = init.context
    apiGroups = Array.isArray(init.apiGroups) ? init.apiGroups : []
    document.documentElement.className = context.theme === 'dark' ? 'dark' : ''
    const style = document.createElement('style')
    style.textContent = init.css || ''
    document.head.append(style)
    window.addEventListener('message', event => {
      if (event.source !== parent || event.origin !== hostOrigin || event.data?.nonce !== nonce || event.data?.type !== 'railgrid-provider-context') return
      context = event.data.context
      publishContext()
    })
  }
  function publishContext() {
    if (!context) return
    const safeFetch = (input, init = {}) => {
      if (input instanceof Request) return fetchBridge(input, init)
      return fetchBridge(input, init)
    }
    const detail = {
      ...context,
      fetch: safeFetch,
      publishPageResource: value => send({ type: 'publish-page-resource', value }),
      publishAgentContext: value => send({ type: 'publish-agent-context', value }),
      ...(context.providerName === 'agents' ? { agentOverlay: {
        ...context.agentOverlay,
        setSettingsOpen: open => send({ type: 'agent-overlay', action: 'setSettingsOpen', value: open }),
        configure: (name, followContext) => send({ type: 'agent-overlay', action: 'configure', value: { name, followContext } }),
        select: name => send({ type: 'agent-overlay', action: 'select', value: name }),
      } } : {}),
    }
    const tag = `railgrid-provider-${context.providerName}`
    const host = document.querySelector(tag)
    if (host) host.railgridContext = detail
  }
  function dispatchHost(name, detail) { send({ type: name, ...detail }) }
  function installBridgeEvents() {
    const root = document.getElementById('provider-root')
    const events = {
      'railgrid-navigate': 'navigate', 'railgrid-layout-change': 'layout-change',
      'railgrid-route-ready': 'route-ready', 'railgrid-provider-bootstrap-retry': 'bootstrap-retry',
      'railgrid-page-resource': 'publish-page-resource', 'railgrid-agent-context': 'publish-agent-context',
      'railgrid-agent-overlay': 'agent-overlay',
      'railgrid-overlay-navigate': 'navigate',
    }
    for (const [eventName, type] of Object.entries(events)) root.addEventListener(eventName, event => {
      const detail = event.detail || {}
      if (type === 'navigate') dispatchHost(type, { path: detail.path, replace: detail.replace === true })
      else if (type === 'layout-change') dispatchHost(type, { fullBleed: detail.fullBleed })
      else if (type === 'publish-page-resource' || type === 'publish-agent-context') dispatchHost(type, { value: detail })
      else if (type === 'agent-overlay') dispatchHost(type, { action: detail.action, value: detail.value })
      else dispatchHost(type, {})
    }, true)
  }
  function mount(init) {
    configure(init)
    installBridgeEvents()
    const scriptURL = URL.createObjectURL(new Blob([init.bytes], { type: 'text/javascript' }))
    const script = document.createElement('script')
    script.src = scriptURL
    script.onload = () => {
      URL.revokeObjectURL(scriptURL)
      const tag = `railgrid-provider-${context.providerName}`
      customElements.whenDefined(tag).then(() => {
        const element = document.createElement(tag)
        document.getElementById('provider-root').replaceChildren(element)
        element.railgridContext = { ...context, fetch: fetchBridge }
        publishContext()
        send({ type: 'ready', nonce })
      })
    }
    script.onerror = () => { URL.revokeObjectURL(scriptURL); send({ type: 'bootstrap-error', nonce }) }
    document.head.append(script)
  }
  window.addEventListener('message', function initial(event) {
    const data = event.data
    if (event.source !== parent || !data || data.type !== 'railgrid-provider-init' || !event.ports?.[0] ||
        typeof data.nonce !== 'string' || typeof data.hostOrigin !== 'string' || event.origin !== data.hostOrigin) return
    window.removeEventListener('message', initial)
    nonce = data.nonce
    hostOrigin = data.hostOrigin
    port = event.ports[0]
    port.onmessage = message => {
      try { consume(message.data) } catch (error) {
        const state = pending.get(message.data?.id)
        if (!state) return
        pending.delete(message.data.id)
        state.signal?.removeEventListener('abort', state.abort)
        const failure = error instanceof Error ? error : new TypeError('Provider response could not be processed.')
        if (state.response) { wakePull(state); state.streamController?.error(failure) }
        else state.reject(failure)
      }
    }
    port.start()
    try { mount({ ...data, context: data.context }) } catch { send({ type: 'bootstrap-error', nonce }) }
  })
})()
