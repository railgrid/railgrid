import { AGENT_BACKEND_MODEL, type AgentBackendType, type HarnessWorkspace } from './types'

export type AgentCredentialFamily = 'chat' | 'harness'

/**
 * Non-secret values needed to recreate AgentCreate after a prerequisite visit.
 * Credential names are references only; credential material is never part of
 * this in-memory setup draft.
 */
export interface AgentCreateDraft {
  name: string
  backendType: AgentBackendType
  modelCredential: string
  harnessEdge: string
  harnessCredential: string
  harnessModel: string
  harnessWorkspace: HarnessWorkspace
  harnessGitHubConnection: string
  systemPrompt: string
  channel: string
  web: boolean
  fanOut: boolean
  visualization: boolean
  webBackground: boolean
  fanOutBackground: boolean
  visualizationBackground: boolean
}

export function emptyAgentCreateDraft(backendType: AgentBackendType = AGENT_BACKEND_MODEL): AgentCreateDraft {
  return {
    name: '',
    backendType,
    modelCredential: '',
    harnessEdge: '',
    harnessCredential: '',
    harnessModel: '',
    harnessWorkspace: 'persistent',
    harnessGitHubConnection: '',
    systemPrompt: '',
    channel: '',
    web: false,
    fanOut: false,
    visualization: false,
    webBackground: false,
    fanOutBackground: false,
    visualizationBackground: false,
  }
}

export function withCreatedCredential(
  draft: AgentCreateDraft,
  family: AgentCredentialFamily,
  name: string,
): AgentCreateDraft {
  return family === 'harness'
    ? { ...draft, backendType: 'harness', harnessCredential: name }
    : { ...draft, backendType: AGENT_BACKEND_MODEL, modelCredential: name }
}
