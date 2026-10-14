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

  it('throws the tool error and the JSON-RPC error as an ApiError', () => {
    expect(() => chatFrameFromRPC({ id: 1, result: { isError: true, content: [{ type: 'text', text: 'agents.agents.railgrid.ai "scout" not found' }] } }))
      .toThrow(ApiError)
    expect(() => chatFrameFromRPC({ id: 1, result: { isError: true, content: [{ type: 'text', text: 'no such agent' }] } }))
      .toThrow('no such agent')
    expect(() => chatFrameFromRPC({ id: 1, error: { code: -32602, message: 'invalid params' } })).toThrow('invalid params')
  })
})
