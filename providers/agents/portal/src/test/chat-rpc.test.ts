// The chat turn rides the provider's MCP endpoint: progress notifications
// carry the events the verb used to stream, and the call's result is the
// terminal frame. chatFrameFromRPC is what turns that back into SSEEvents.
import { describe, expect, it } from 'vitest'

import { ApiError, chatFrameFromRPC } from '../api'

describe('chatFrameFromRPC', () => {
  it('maps a progress notification to the chat event in its _meta', () => {
    expect(chatFrameFromRPC({
      jsonrpc: '2.0', method: 'notifications/progress',
      params: { progressToken: 1, progress: 3, _meta: { event: 'delta', data: { text: 'hel' } } },
    })).toEqual({ event: 'delta', data: { text: 'hel' } })
  })

  it('ignores progress for another token and messages that are not chat frames', () => {
    expect(chatFrameFromRPC({ method: 'notifications/progress', params: { progressToken: 'other', _meta: { event: 'delta', data: {} } } })).toBeNull()
    expect(chatFrameFromRPC({ method: 'notifications/progress', params: { progressToken: 1, progress: 1 } })).toBeNull()
    expect(chatFrameFromRPC({ method: 'notifications/message', params: { level: 'info', data: 'x' } })).toBeNull()
    expect(chatFrameFromRPC('keepalive')).toBeNull()
    expect(chatFrameFromRPC(null)).toBeNull()
  })

  it('maps the structured result to the terminal frame', () => {
    expect(chatFrameFromRPC({
      jsonrpc: '2.0', id: 1,
      result: { content: [{ type: 'text', text: '{}' }], structuredContent: { event: 'done', data: { runID: 'r1', status: 'completed' } } },
    })).toEqual({ event: 'done', data: { runID: 'r1', status: 'completed' } })
  })

  it('throws a classified refusal with the status the verb would have answered', () => {
    const notFound = {
      id: 1,
      result: {
        isError: true,
        content: [{ type: 'text', text: 'agents.agents.railgrid.ai "scout" not found' }],
        structuredContent: { event: 'error', data: { message: 'agents.agents.railgrid.ai "scout" not found', code: 404, reason: 'NotFound' } },
      },
    }
    expect(() => chatFrameFromRPC(notFound)).toThrow(ApiError)
    try {
      chatFrameFromRPC(notFound)
    } catch (e) {
      expect((e as ApiError).status).toBe(404)
      expect((e as ApiError).reason).toBe('NotFound')
    }
    try {
      chatFrameFromRPC({ id: 1, result: { isError: true, content: [{ type: 'text' }], structuredContent: { event: 'error', data: { message: 'message is required', code: 400, reason: 'BadRequest' } } } })
    } catch (e) {
      expect((e as ApiError).status).toBe(400)
    }
  })

  it('reads an unclassified tool error and a JSON-RPC error as upstream', () => {
    try {
      chatFrameFromRPC({ id: 1, result: { isError: true, content: [{ type: 'text', text: 'no such agent' }] } })
    } catch (e) {
      expect((e as ApiError).status).toBe(502)
    }
    expect(() => chatFrameFromRPC({ id: 1, error: { code: -32602, message: 'invalid params' } })).toThrow(ApiError)
  })
})
