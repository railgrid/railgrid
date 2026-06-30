export interface ProjectCreateReadiness {
  gitConnection: {
    ready: boolean
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

const defaultGitConnectionMessage = 'You need to connect to a Git account before you can continue'

export function gitConnectionReady(readiness: ProjectCreateReadiness | null): boolean {
  return readiness?.gitConnection.ready === true
}

export function createPromptBlockedMessage(readiness: ProjectCreateReadiness | null): string {
  if (gitConnectionReady(readiness)) return ''
  return readiness?.gitConnection.message?.trim() || defaultGitConnectionMessage
}

export function canSubmitCreatePrompt(prompt: string, readiness: ProjectCreateReadiness | null): boolean {
  return prompt.trim().length > 0 && gitConnectionReady(readiness)
}

export function createSetupItems(input: CreateSetupItemsInput): CreateSetupItem[] {
  const gitReady = gitConnectionReady(input.readiness)
  if (gitReady && input.llmConfigured) return []

  const items: CreateSetupItem[] = [gitSetupItem(gitReady, input.checkingGit)]

  items.push(
    input.llmConfigured
      ? {
        id: 'llm',
        label: 'LLM credentials',
        status: 'ready',
      }
      : {
        id: 'llm',
        label: 'LLM credentials',
        status: 'missing',
        actionLabel: 'Set up LLM',
        action: 'setup-llm',
      },
  )

  return items
}

function gitSetupItem(gitReady: boolean, checkingGit: boolean): CreateSetupItem {
  if (checkingGit) {
    return {
      id: 'git',
      label: 'Git connection',
      status: 'checking',
    }
  }
  if (gitReady) {
    return {
      id: 'git',
      label: 'Git connection',
      status: 'ready',
    }
  }
  return {
    id: 'git',
    label: 'Git connection',
    status: 'missing',
    actionLabel: 'Connect Git',
    action: 'connect-git',
  }
}
