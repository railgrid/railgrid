import type { ConfirmOptions } from './portalkit/confirm'
import type { Repository } from './types'

export interface RepositoryDeletionPresentation {
  confirmation: ConfirmOptions
  actionLabel: string
  pendingLabel: string
}

type RepositoryDeletionSnapshot = Pick<Repository, 'name' | 'repo' | 'owner' | 'existingOnly' | 'retainsRemote'>

export function repositoryDeletionPresentation(repository: RepositoryDeletionSnapshot): RepositoryDeletionPresentation {
  if (repository.retainsRemote === true) {
    const remoteName = repository.owner ? `${repository.owner}/${repository.repo}` : repository.repo || repository.name
    return {
      confirmation: {
        title: 'Remove repository registration?',
        message: `This removes the Railgrid registration for ${remoteName}. The GitHub repository is retained.`,
        confirmLabel: 'Remove registration',
        danger: true,
      },
      actionLabel: 'Remove registration',
      pendingLabel: 'Removing registration',
    }
  }

  return {
    confirmation: {
      title: `Delete repository "${repository.repo}"?`,
      message: repository.existingOnly
        ? 'Railgrid has not confirmed that the GitHub repository will be retained. This action may delete it permanently.'
        : 'This removes the repository on the git host. This cannot be undone.',
      confirmLabel: 'Delete',
      danger: true,
    },
    actionLabel: 'Delete repository',
    pendingLabel: 'Deleting repository',
  }
}
