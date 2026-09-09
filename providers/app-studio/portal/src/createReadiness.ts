export interface ProjectCreateReadiness {
  gitConnection: {
    ready: boolean
    status?: 'ready' | 'provider-missing' | 'connection-missing' | 'validating' | 'failed'
    connectionRef?: string
    message?: string
  }
}

export interface CreateSetupItemsInput {
  readiness: ProjectCreateReadiness | null
  llmConfigured: boolean
  checkingGit: boolean
}

export interface CreateSetupItem {
  id: 'git' | 'llm'
  label: string
  status: 'checking' | 'ready' | 'missing'
  actionLabel?: string
  action?: 'connect-git' | 'setup-llm'
}

// Git readiness is advisory; model setup remains the required creation gate.
export function gitConnectionReady(readiness: ProjectCreateReadiness | null): boolean {
  return readiness?.gitConnection.ready === true
}

export function createPromptBlockedMessage(_readiness: ProjectCreateReadiness | null): string {
  return ''
}

export function canSubmitCreatePrompt(prompt: string, _readiness: ProjectCreateReadiness | null): boolean {
  return prompt.trim().length > 0
}

export function createSetupItems(input: CreateSetupItemsInput): CreateSetupItem[] {
  return input.llmConfigured ? [] : [{
    id: 'llm', label: 'LLM credentials', status: 'missing',
    actionLabel: 'Set up LLM', action: 'setup-llm',
  }]
}
