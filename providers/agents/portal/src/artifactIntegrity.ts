import type { AgentArtifact, AgentArtifactAppIdentity, AgentArtifactMcpApp } from './types'

export type ArtifactIdentity = Pick<AgentArtifact,
  'id' | 'agentName' | 'agentUID' | 'owner' | 'runID' | 'sessionID' | 'toolCallID' | 'digest'>

export type ArtifactIdentityExpectation = Partial<ArtifactIdentity>

/** Compare only receipt fields the caller has independently observed. */
export function artifactIdentityMatches(expected: ArtifactIdentityExpectation, actual: AgentArtifact): boolean {
  const keys: (keyof ArtifactIdentity)[] = ['id', 'agentName', 'agentUID', 'owner', 'runID', 'sessionID', 'toolCallID', 'digest']
  return keys.every(key => expected[key] === undefined || expected[key] === actual[key])
}

export function artifactMetadataForRevalidation(artifact: AgentArtifact): AgentArtifact {
  const metadata = { ...artifact }
  delete metadata.data
  delete metadata.input
  delete metadata.providerPresentation
  metadata.evidenceStatus = metadata.sources.length ? 'source_revalidation_required' : 'captured_unverified'
  return metadata
}

function subtleCrypto(): SubtleCrypto | null {
  return globalThis.crypto?.subtle || null
}

export async function sha256Hex(value: string): Promise<string | null> {
  const subtle = subtleCrypto()
  if (!subtle) return null
  try {
    const digest = await subtle.digest('SHA-256', new TextEncoder().encode(value))
    return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
  } catch {
    return null
  }
}

export interface ArtifactContentDigests {
  data?: string
  digest: string
  input?: string
  inputDigest?: string
}

/** Hash the exact UTF-8 strings returned by the backend before displaying/sending them. */
export async function verifyArtifactContentDigests(
  artifact: ArtifactContentDigests,
  options: { requireData?: boolean; requireInputForDigest?: boolean } = {},
): Promise<boolean> {
  const requireData = options.requireData ?? true
  if (requireData && typeof artifact.data !== 'string') return false
  if (artifact.data !== undefined && await sha256Hex(artifact.data) !== artifact.digest) return false
  if (artifact.inputDigest !== undefined) {
    if (artifact.input === undefined) return !(options.requireInputForDigest ?? true)
    if (await sha256Hex(artifact.input) !== artifact.inputDigest) return false
  }
  return true
}

/** The attended backend pins these fields against its operator-reviewed provider registry. */
export function mcpAppIdentityMatches(app: AgentArtifactMcpApp, identity: AgentArtifactAppIdentity | undefined): boolean {
  if (!identity || app.resourceUri.length > 4096 || app.resourceUri !== identity.resourceUri || app.sha256 !== identity.sha256) return false
  if (!/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(identity.provider)) return false
  const providerMatch = /^ui:\/\/railgrid-provider\/([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)\/[A-Za-z0-9_-]+$/.exec(app.resourceUri)
  return providerMatch?.[1] === identity.provider
}
