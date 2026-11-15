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

// A focus refresh can supersede the request started by submission. Wait for
// whichever request is current before evaluating its result.
export async function waitForCurrentReadiness(getCurrent: () => Promise<void> | null): Promise<void> {
  while (getCurrent()) await getCurrent()
}

// Git readiness is advisory; model setup remains the required creation gate.
export function gitConnectionReady(readiness: ProjectCreateReadiness | null): boolean {
  return readiness?.gitConnection.ready === true
}

// Rechecking may discover a replacement, but never silently switches a selected
// connection. An unchecked choice can adopt the available reference for later
// selection, and a successful check clears the stale failure message.
export function reconcileGitReview(withGit: boolean, reviewed: string, error: string, readiness: ProjectCreateReadiness | null) {
  const current = gitConnectionReady(readiness) ? readiness?.gitConnection.connectionRef || '' : ''
  if (!withGit) return { reviewed: current, error: current ? '' : error }
  return { reviewed, error: current && current === reviewed ? '' : error }
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
