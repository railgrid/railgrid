import type { Repository } from './types'

export function repositoryDeletionMessage(repository: Repository): string {
  if (repository.existingOnly === true) return 'This removes the Railgrid registration. Its current configuration protects the repository on the git host from deletion.'
  if (repository.existingOnly === false) return 'This removes the Railgrid registration and may delete the repository on the git host. Git host deletion cannot be undone.'
  return 'This removes the Railgrid registration and may also remove the repository on the git host. Git host deletion cannot be undone.'
}
