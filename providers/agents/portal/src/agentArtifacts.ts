import type { AgentArtifact, AgentArtifactMcpApp, AgentArtifactSource, SavedCaptureCitation } from './types'

const MAX_BYTES = 1 << 20
const MAX_SOURCES = 32
const MAX_ACTIONS = 8
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const bytes = (value: string): number => new TextEncoder().encode(value).byteLength
const bounded = (value: unknown, max: number): value is string => typeof value === 'string' && value.length <= max

function exactKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  const keys = Object.keys(value).sort()
  const expected = [...allowed].sort()
  return keys.length === expected.length && keys.every((key, index) => key === expected[index])
}

/** Parse only the server-attested citation envelope, with a bounded subset. */
export function parseSavedCaptureCitation(value: unknown): SavedCaptureCitation | null {
  if (!record(value) || Object.keys(value).some(key => !['artifactID', 'digest', 'selection'].includes(key))) return null
  if (typeof value.artifactID !== 'string' || !value.artifactID || value.artifactID.trim() !== value.artifactID || bytes(value.artifactID) > 128) return null
  if (typeof value.digest !== 'string' || !/^[0-9a-f]{64}$/.test(value.digest)) return null
  if (!Object.hasOwn(value, 'selection')) {
    return exactKeys(value, ['artifactID', 'digest']) ? { artifactID: value.artifactID, digest: value.digest } : null
  }
  if (!exactKeys(value, ['artifactID', 'digest', 'selection']) || !record(value.selection) || !exactKeys(value.selection, ['rowIndices', 'fields'])) return null
  const { rowIndices, fields } = value.selection
  if (!Array.isArray(rowIndices) || rowIndices.length < 1 || rowIndices.length > 250 ||
      rowIndices.some(index => !Number.isInteger(index) || index < 0 || index > 249) || new Set(rowIndices).size !== rowIndices.length) return null
  if (!Array.isArray(fields) || fields.length < 1 || fields.length > 24 ||
      fields.some(field => typeof field !== 'string' || !field || field.trim() !== field || bytes(field) > 256) || new Set(fields).size !== fields.length) return null
  return { artifactID: value.artifactID, digest: value.digest, selection: { rowIndices: [...rowIndices] as number[], fields: [...fields] as string[] } }
}

function parseSource(value: unknown): AgentArtifactSource | null {
  if (!record(value) || !record(value.object) || !Array.isArray(value.access) || value.access.length > MAX_ACTIONS) return null
  const obj = value.object
  if (!bounded(obj.version, 64) || !obj.version || !bounded(obj.resource, 128) || !obj.resource || !bounded(obj.name, 253) || !obj.name || !bounded(obj.uid, 128) || !obj.uid) return null
  if (obj.group !== undefined && !bounded(obj.group, 253)) return null
  if (obj.namespace !== undefined && !bounded(obj.namespace, 253)) return null
  const access: AgentArtifactSource['access'] = []
  for (const item of value.access) {
    if (!record(item) || !bounded(item.version, 64) || !item.version || !bounded(item.resource, 128) || !item.resource || !bounded(item.verb, 32) || !item.verb) return null
    for (const key of ['group', 'subresource', 'namespace', 'name'] as const) if (item[key] !== undefined && !bounded(item[key], 253)) return null
    access.push({
      ...(typeof item.group === 'string' ? { group: item.group } : {}), version: item.version, resource: item.resource,
      ...(typeof item.subresource === 'string' ? { subresource: item.subresource } : {}),
      ...(typeof item.namespace === 'string' ? { namespace: item.namespace } : {}),
      ...(typeof item.name === 'string' ? { name: item.name } : {}), verb: item.verb,
    })
  }
  return {
    ...(bounded(value.label, 256) ? { label: value.label } : {}),
    object: { ...(typeof obj.group === 'string' ? { group: obj.group } : {}), version: obj.version, resource: obj.resource,
      ...(typeof obj.namespace === 'string' ? { namespace: obj.namespace } : {}), name: obj.name, uid: obj.uid },
    access,
  }
}

export function parseAgentArtifactSources(value: unknown): AgentArtifactSource[] | null {
  if (!Array.isArray(value) || value.length > MAX_SOURCES) return null
  const sources: AgentArtifactSource[] = []
  for (const item of value) {
    const source = parseSource(item)
    if (!source) return null
    sources.push(source)
  }
  return sources
}

export function parseAgentArtifactMcpApp(value: unknown): AgentArtifactMcpApp | null {
  if (!record(value) || !exactKeys(value, ['resourceUri', 'mimeType', 'html', 'sha256'])) return null
  if (typeof value.resourceUri !== 'string' || !/^ui:\/\/[A-Za-z0-9._~!$&'()*+,;=:@%/-]{1,512}$/.test(value.resourceUri) || value.resourceUri.includes('..')) return null
  if (value.mimeType !== 'text/html;profile=mcp-app' || typeof value.html !== 'string' || !value.html.length || bytes(value.html) > MAX_BYTES) return null
  if (typeof value.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(value.sha256)) return null
  return { resourceUri: value.resourceUri, mimeType: value.mimeType, html: value.html, sha256: value.sha256 }
}

export function parseAgentArtifact(value: unknown): AgentArtifact | null {
  if (!record(value)) return null
  const required = ['id', 'agentName', 'agentUID', 'owner', 'runID', 'sessionID', 'toolCallID', 'toolName', 'title', 'mediaType', 'digest', 'evidenceStatus', 'createdAt'] as const
  if (required.some(key => typeof value[key] !== 'string' || (value[key] as string).length > 1024)) return null
  if (!Number.isSafeInteger(value.size) || (value.size as number) < 0 || (value.size as number) > MAX_BYTES) return null
  if (!/^[0-9a-f]{64}$/.test(value.digest as string)) return null
  if (value.data !== undefined && (typeof value.data !== 'string' || bytes(value.data) > MAX_BYTES)) return null
  if (value.input !== undefined && (typeof value.input !== 'string' || bytes(value.input) > 16 * 1024)) return null
  const sources = value.sources === undefined ? [] : parseAgentArtifactSources(value.sources)
  if (!sources) return null
  return {
    id: value.id as string, agentName: value.agentName as string, agentUID: value.agentUID as string, owner: value.owner as string,
    runID: value.runID as string, sessionID: value.sessionID as string, toolCallID: value.toolCallID as string,
    toolName: value.toolName as string, title: value.title as string, mediaType: value.mediaType as string,
    digest: value.digest as string, size: value.size as number, sources, evidenceStatus: value.evidenceStatus as string,
    createdAt: value.createdAt as string, ...(typeof value.data === 'string' ? { data: value.data } : {}),
    ...(typeof value.input === 'string' ? { input: value.input } : {}),
  }
}
