import { describe, expect, it } from 'vitest'
import { repositoryDeletionMessage } from './repositoryDeletion'
import type { Repository } from './types'

const repository: Repository = { name: 'orders', connectionRef: 'github', repo: 'orders', visibility: 'private', ready: true }

describe('repository deletion wording', () => {
  it('describes the current protection for an imported repository', () => {
    expect(repositoryDeletionMessage({ ...repository, existingOnly: true })).toContain('current configuration protects')
  })
  it('warns when a managed repository can be deleted remotely', () => {
    expect(repositoryDeletionMessage({ ...repository, existingOnly: false })).toContain('may delete the repository on the git host')
  })
  it('does not promise protection when the policy is unknown', () => {
    expect(repositoryDeletionMessage(repository)).toContain('may also remove the repository on the git host')
  })
})
