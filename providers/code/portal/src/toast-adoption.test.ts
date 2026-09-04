import { describe, expect, it } from 'vitest'

import connectionCreate from './views/ConnectionCreateView.vue?raw'
import connectionDetail from './views/ConnectionDetailView.vue?raw'
import connections from './views/ConnectionsView.vue?raw'
import repositoryCreate from './views/RepositoryCreateView.vue?raw'
import repositoryDetail from './views/RepoDetailView.vue?raw'
import repositories from './views/RepositoriesView.vue?raw'

const sources = [connectionCreate, connectionDetail, connections, repositoryCreate, repositoryDetail, repositories]

describe('Code toast adoption', () => {
  it('covers every silent-success mutation entry point with the Vue transport', () => {
    for (const source of sources) expect(source).toContain("import { toast } from '../portalkit/toast'")
    expect(sources.join('\n').match(/\btoast\(/g)).toHaveLength(11)
    expect(connectionCreate).toContain("toast('info', `Connection creation requested for ${created.name}.`)")
    expect(repositoryCreate).toContain("toast('info', `Repository creation requested for ${created.name}.`)")
    expect(repositoryDetail).toContain("toast('info', `Repository connection update requested for ${current.name}.`)")
    expect(repositoryDetail.match(/toast\('info', `Deploy key update requested/g)).toHaveLength(2)
    expect(repositoryDetail.match(/toast\('info', `Collaborator update requested/g)).toHaveLength(2)
  })

  it('uses accepted-request language and never duplicates contextual failures as error toasts', () => {
    const source = sources.join('\n')
    expect(source).not.toContain("toast('error'")
    expect(source.match(/deletion requested/g)).toHaveLength(4)
    expect(source).not.toMatch(/toast\([^\n]+(deleted|created successfully)/i)
  })
})
