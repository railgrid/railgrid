import { describe, expect, it } from 'vitest'

import { repositoryDeletionPresentation } from './repositoryDeletion'
import type { Repository } from './types'
import collectionSource from './views/RepositoriesView.vue?raw'
import detailSource from './views/RepoDetailView.vue?raw'

const importedRepository: Repository = {
  name: 'railgrid-orders',
  uid: 'orders-uid',
  connectionRef: 'github',
  repo: 'orders',
  owner: 'railgrid',
  existingOnly: true,
  retainsRemote: true,
  visibility: 'private',
  ready: true,
}

describe('repository deletion presentation', () => {
  it('explains that removing an imported repository retains its GitHub repository', () => {
    expect(repositoryDeletionPresentation(importedRepository)).toEqual({
      confirmation: {
        title: 'Remove repository registration?',
        message: 'This removes the Railgrid registration for railgrid/orders. The GitHub repository is retained.',
        confirmLabel: 'Remove registration',
        danger: true,
      },
      actionLabel: 'Remove registration',
      pendingLabel: 'Removing registration',
    })
  })

  it('does not promise retention from a legacy import annotation alone', () => {
    expect(repositoryDeletionPresentation({ ...importedRepository, retainsRemote: false }).confirmation.message)
      .toContain('may delete it permanently')
  })

  it.each([false, undefined])('preserves the irreversible GitHub deletion warning when existing-only is %s', existingOnly => {
    expect(repositoryDeletionPresentation({ ...importedRepository, existingOnly, retainsRemote: false })).toEqual({
      confirmation: {
        title: 'Delete repository "orders"?',
        message: 'This removes the repository on the git host. This cannot be undone.',
        confirmLabel: 'Delete',
        danger: true,
      },
      actionLabel: 'Delete repository',
      pendingLabel: 'Deleting repository',
    })
  })

  it('keeps list and detail deletes behind the shared presentation confirmation', () => {
    expect(collectionSource).toMatch(/const ok = await confirmDialog\(repositoryDeletionPresentation\(repository\)\.confirmation\)[\s\S]*?if \(!ok \|\| !mounted\) return[\s\S]*?await api\.deleteRepository\(repository\)/)
    expect(detailSource).toMatch(/const ok = await confirmDialog\(repositoryDeletionPresentation\(current\)\.confirmation\)[\s\S]*?if \(!ok \|\| !mounted\) return[\s\S]*?await api\.deleteRepository\(current\)/)
  })
})
