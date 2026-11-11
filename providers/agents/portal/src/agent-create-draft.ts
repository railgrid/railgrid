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
  harnessReasoningEffort: string
  harnessWorkspace: HarnessWorkspace
  harnessGitHubConnection: string
  systemPrompt: string
  customPrompt: boolean
  channel: string
  // The template the form started from, with its parameter values and the
  // connection-slot wiring that has no field of its own.
  template: string
  parameters: Record<string, string>
  wiring: Record<string, string>
  researchSpecialist?: boolean
  digestTimeMissing?: boolean
  templateDrafts?: Record<string, AgentCreateDraft>
}

export function emptyAgentCreateDraft(backendType: AgentBackendType = AGENT_BACKEND_MODEL, template = ''): AgentCreateDraft {
  return {
    name: '',
    backendType,
    modelCredential: '',
    harnessEdge: '',
    harnessCredential: '',
    harnessModel: '',
    harnessReasoningEffort: '',
    harnessWorkspace: 'persistent',
    harnessGitHubConnection: '',
    systemPrompt: '',
    customPrompt: false,
    channel: '',
    template,
    parameters: {},
    wiring: {},
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
