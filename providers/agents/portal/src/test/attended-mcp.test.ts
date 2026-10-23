import { describe, expect, it, vi } from 'vitest'
import { callAttendedMcpTool, MAX_ATTENDED_MCP_RESPONSE_BYTES } from '../attendedMcp'
import {
  MAX_AGENT_ARTIFACT_ACTIONS,
  MAX_AGENT_ARTIFACT_DATA_BYTES,
  MAX_AGENT_ARTIFACT_HTML_BYTES,
  MAX_AGENT_ARTIFACT_INPUT_BYTES,
  MAX_AGENT_ARTIFACT_SOURCES,
} from '../agentArtifacts'

const options = (fetcher: typeof fetch, extra: Partial<Parameters<typeof callAttendedMcpTool>[0]> = {}) => ({
  fetcher,
  url: '/services/providers/agents/mcp',
  headers: { 'Content-Type': 'application/json' },
  requestID: 7,
  name: 'get_agent_artifact',
  args: { agent: 'scout', artifactID: 'opaque' },
  ...extra,
})

function goJSONStringify(value: unknown): string {
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, char => `\\u${char.charCodeAt(0).toString(16).padStart(4, '0')}`)
}

function hostileCodeUnits(length: number): string {
  const pair = '<\u0000'
  return pair.repeat(Math.floor(length / pair.length)) + (length % pair.length ? '<' : '')
}

describe('attended MCP transport', () => {
  it('parses bounded JSON results and carries MCP metadata separately', async () => {
    const response = new Response(JSON.stringify({
      jsonrpc: '2.0', id: 7, result: { structuredContent: { artifact: { id: 'opaque' } }, _meta: { railgrid: { mcpApp: 'private' } } },
    }), { headers: { 'Content-Type': 'application/json' } })
    const call = await callAttendedMcpTool(options(vi.fn(async () => response)))
    expect(call.output).toEqual({ artifact: { id: 'opaque' } })
    expect(call.metadata).toEqual({ railgrid: { mcpApp: 'private' } })
  })

  it('selects the matching JSON-RPC result from SSE frames', async () => {
    const body = `event: message\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: 7, result: { structuredContent: { artifact: { id: 'sse' } } } })}\n\n`
    const response = new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
    await expect(callAttendedMcpTool(options(vi.fn(async () => response)))).resolves.toMatchObject({ output: { artifact: { id: 'sse' } } })
  })

  it('preserves multiline SSE data when CRLF pairs are split across chunks', async () => {
    const rpc = JSON.stringify({ jsonrpc: '2.0', id: 7, result: { structuredContent: { artifact: { id: 'split-crlf' } } } })
    const split = rpc.indexOf(',"result"')
    const chunks = [
      `event: message\r\nid: 7\r\ndata: ${rpc.slice(0, split)}\r`,
      `\ndata: ${rpc.slice(split)}\r\n\r`,
      '\n',
    ]
    const response = new Response(new ReadableStream<Uint8Array>({
      start(controller) {
        const encoder = new TextEncoder()
        for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
        controller.close()
      },
    }), { headers: { 'Content-Type': 'text/event-stream' } })
    await expect(callAttendedMcpTool(options(vi.fn(async () => response))))
      .resolves.toMatchObject({ output: { artifact: { id: 'split-crlf' } } })
  })

  it('rejects responses larger than the parser-derived bound', async () => {
    const response = new Response('{}', {
      headers: { 'Content-Type': 'application/json', 'Content-Length': String(MAX_ATTENDED_MCP_RESPONSE_BYTES + 1) },
    })
    await expect(callAttendedMcpTool(options(vi.fn(async () => response)))).rejects.toMatchObject({
      status: 502,
      message: expect.stringContaining('oversized'),
    })
  })

  it('bounds a Go-escaped maximum payload with structured, nested text, and metadata copies', () => {
    const sourceCodeUnits = MAX_AGENT_ARTIFACT_SOURCES * (
      256 + 253 + 64 + 128 + 253 + 253 + 128 +
      MAX_AGENT_ARTIFACT_ACTIONS * (253 + 64 + 128 + 253 + 253 + 253 + 32)
    )
    const metadataCodeUnits = 16 * 1024
    const envelopeHeadroom = 256 * 1024
    const nonHTMLCodeUnits = MAX_AGENT_ARTIFACT_DATA_BYTES + MAX_AGENT_ARTIFACT_INPUT_BYTES + sourceCodeUnits + metadataCodeUnits
    expect(MAX_ATTENDED_MCP_RESPONSE_BYTES).toBe(
      6 * MAX_AGENT_ARTIFACT_HTML_BYTES + (6 + 7) * nonHTMLCodeUnits + envelopeHeadroom,
    )

    const requiredFields = [
      'id', 'agentName', 'agentUID', 'owner', 'runID', 'sessionID', 'toolCallID', 'toolName', 'title',
      'mediaType', 'digest', 'evidenceStatus', 'createdAt',
    ] as const
    const artifact: Record<string, unknown> = Object.fromEntries(requiredFields.map(field => [field, hostileCodeUnits(1024)]))
    artifact.digest = 'a'.repeat(64)
    const usedMetadataUnits = requiredFields.length * 1024 - (1024 - 64)
    artifact.extraMetadata = hostileCodeUnits(metadataCodeUnits - usedMetadataUnits)
    artifact.size = MAX_AGENT_ARTIFACT_DATA_BYTES
    artifact.data = hostileCodeUnits(MAX_AGENT_ARTIFACT_DATA_BYTES)
    artifact.input = hostileCodeUnits(MAX_AGENT_ARTIFACT_INPUT_BYTES)
    artifact.sources = Array.from({ length: MAX_AGENT_ARTIFACT_SOURCES }, () => ({
      label: hostileCodeUnits(256),
      object: {
        group: hostileCodeUnits(253), version: hostileCodeUnits(64), resource: hostileCodeUnits(128),
        namespace: hostileCodeUnits(253), name: hostileCodeUnits(253), uid: hostileCodeUnits(128),
      },
      access: Array.from({ length: MAX_AGENT_ARTIFACT_ACTIONS }, () => ({
        group: hostileCodeUnits(253), version: hostileCodeUnits(64), resource: hostileCodeUnits(128),
        subresource: hostileCodeUnits(253), namespace: hostileCodeUnits(253), name: hostileCodeUnits(253), verb: hostileCodeUnits(32),
      })),
    }))
    const structuredContent = { artifact }
    const embeddedResult = goJSONStringify(structuredContent)
    const responseText = goJSONStringify({
      jsonrpc: '2.0', id: 7, result: {
        structuredContent,
        content: [{ type: 'text', text: embeddedResult }],
        _meta: { railgrid: { mcpApp: { html: hostileCodeUnits(MAX_AGENT_ARTIFACT_HTML_BYTES) } } },
      },
    })
    const actualBytes = new TextEncoder().encode(responseText).byteLength
    expect(actualBytes).toBeLessThanOrEqual(MAX_ATTENDED_MCP_RESPONSE_BYTES)
    expect(actualBytes).toBeGreaterThan(MAX_ATTENDED_MCP_RESPONSE_BYTES - envelopeHeadroom)
  })

  it('aborts promptly when the calling inspector loses authority', async () => {
    const authority = new AbortController()
    const fetcher = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) => new Promise<Response>(() => undefined))
    const call = callAttendedMcpTool(options(fetcher, { signal: authority.signal }))
    authority.abort()
    await expect(call).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).toHaveBeenCalledOnce()
  })

})
