import { afterEach, describe, expect, it, vi } from 'vitest'
import { webcrypto } from 'node:crypto'
import { artifactMetadataForRevalidation, mcpAppIdentityMatches, verifyArtifactContentDigests } from '../artifactIntegrity'
import type { AgentArtifact, AgentArtifactMcpApp } from '../types'

const sha256OfAB = 'fb8e20fc2e4c3f248c60c39bd652f3c1347298bb977b8b4d5903b85055620603'
const sha256OfXY = '769a4e6d0003189c7e96c5d9b7e810a0d11c3a12832527ec94b0f86d277f51ca'

afterEach(() => vi.unstubAllGlobals())

describe('artifact integrity and trust pins', () => {
  it('verifies exact result and input UTF-8 digests and rejects corruption', async () => {
    vi.stubGlobal('crypto', webcrypto)
    const valid = { data: 'ab', digest: sha256OfAB, input: 'xy', inputDigest: sha256OfXY }
    expect(await verifyArtifactContentDigests(valid)).toBe(true)
    expect(await verifyArtifactContentDigests({ ...valid, data: 'changed' })).toBe(false)
    expect(await verifyArtifactContentDigests({ ...valid, input: 'changed' })).toBe(false)
    expect(await verifyArtifactContentDigests({ data: 'ab', digest: sha256OfAB, input: 'xy' })).toBe(false)
    expect(await verifyArtifactContentDigests({ data: 'ab', digest: sha256OfAB })).toBe(true)
  })

  it('requires an independently pinned canonical provider resource for an app', () => {
    const identity = { provider: 'semantic-ai', resourceUri: 'ui://railgrid-provider/semantic-ai/YmFzZTY0', sha256: 'a'.repeat(64) }
    const app: AgentArtifactMcpApp = { resourceUri: identity.resourceUri, mimeType: 'text/html;profile=mcp-app', html: '<main />', sha256: identity.sha256 }
     expect(mcpAppIdentityMatches(app, identity)).toBe(true)
     expect(mcpAppIdentityMatches(app, undefined)).toBe(false)
     expect(mcpAppIdentityMatches({ ...app, resourceUri: 'ui://railgrid-provider/other/YmFzZTY0' }, identity)).toBe(false)
     expect(mcpAppIdentityMatches({ ...app, sha256: 'b'.repeat(64) }, identity)).toBe(false)
     expect(mcpAppIdentityMatches({ ...app, resourceUri: 'ui://semantic-ai/v1/result' }, identity)).toBe(false)
  })

  it('strips captured bytes and app HTML before caching metadata', () => {
    const artifact = {
      id: 'capture', agentName: 'agent', agentUID: 'uid', owner: 'user', runID: 'run', sessionID: 'session', toolCallID: 'call',
      toolName: 'query', title: 'Result', mediaType: 'application/json', digest: 'a'.repeat(64), size: 1,
      sources: [{ object: { version: 'v1', resource: 'items', name: 'x', uid: 'source' }, access: [] }],
      evidenceStatus: 'source_identity_and_access_revalidated', createdAt: '', data: '{}', input: '{}',
      providerPresentation: { mcpApp: { resourceUri: 'ui://railgrid-provider/p/YQ', mimeType: 'text/html;profile=mcp-app', html: 'secret', sha256: 'b'.repeat(64) } },
    } as AgentArtifact
    const metadata = artifactMetadataForRevalidation(artifact)
    expect(metadata).not.toHaveProperty('data')
    expect(metadata).not.toHaveProperty('input')
    expect(metadata).not.toHaveProperty('providerPresentation')
    expect(metadata.evidenceStatus).toBe('source_revalidation_required')
  })
})

it('accepts long canonical resource identifiers within the Hub URI limit', () => {
  const resourceUri = 'ui://railgrid-provider/reports/' + 'Y'.repeat(2000)
  const app: AgentArtifactMcpApp = { resourceUri, sha256: 'a'.repeat(64), html: '<main/>', mimeType: 'text/html;profile=mcp-app' }
  expect(mcpAppIdentityMatches(app, {provider: 'reports', resourceUri, sha256: app.sha256})).toBe(true)
  expect(mcpAppIdentityMatches({...app,resourceUri:resourceUri+'Y'.repeat(4096)}, {provider:'reports',resourceUri:resourceUri+'Y'.repeat(4096),sha256:app.sha256})).toBe(false)
})
