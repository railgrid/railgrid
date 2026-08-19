import type { ProjectRepositoryCommit } from './types'

function clean(value: string | null | undefined): string {
  return value?.trim() ?? ''
}

function commitTime(commit: ProjectRepositoryCommit): number {
  for (const value of [commit.completedAt, commit.createdAt]) {
    const parsed = Date.parse(clean(value))
    if (Number.isFinite(parsed)) return parsed
  }
  return 0
}

export function repositoryCommitSelectable(commit: ProjectRepositoryCommit | null | undefined): commit is ProjectRepositoryCommit & { commitSHA: string } {
  const sha = clean(commit?.commitSHA)
  return commit?.phase === 'Succeeded' && (sha.length === 40 || sha.length === 64) && /^[0-9a-f]+$/i.test(sha)
}

export function orderRepositoryCommits(commits: readonly ProjectRepositoryCommit[]): ProjectRepositoryCommit[] {
  return commits
    .map((commit, index) => ({ commit, index }))
    .sort((left, right) => commitTime(right.commit) - commitTime(left.commit) || left.index - right.index)
    .map(({ commit }) => commit)
}

export function reconcileHistorySelection(previous: string | null | undefined, commits: readonly ProjectRepositoryCommit[]): string {
  const selected = clean(previous)
  const ordered = orderRepositoryCommits(commits)
  if (selected && ordered.some((commit) => repositoryCommitSelectable(commit) && clean(commit.commitSHA) === selected)) return selected
  return clean(ordered.find(repositoryCommitSelectable)?.commitSHA)
}

export function selectedHistoryCommit(commits: readonly ProjectRepositoryCommit[], commitSHA: string | null | undefined): ProjectRepositoryCommit | null {
  const selected = clean(commitSHA)
  return orderRepositoryCommits(commits).find((commit) => repositoryCommitSelectable(commit) && clean(commit.commitSHA) === selected) ?? null
}

export function adjacentHistoryCommit(
  commits: readonly ProjectRepositoryCommit[],
  commitSHA: string | null | undefined,
  direction: 'next' | 'previous' | 'first' | 'last',
): ProjectRepositoryCommit | null {
  const selectable = orderRepositoryCommits(commits).filter(repositoryCommitSelectable)
  if (selectable.length === 0) return null
  if (direction === 'first') return selectable[0] ?? null
  if (direction === 'last') return selectable[selectable.length - 1] ?? null
  const current = selectable.findIndex((commit) => clean(commit.commitSHA) === clean(commitSHA))
  if (current < 0) return selectable[0] ?? null
  const offset = direction === 'next' ? 1 : -1
  return selectable[(current + offset + selectable.length) % selectable.length] ?? null
}

export function formatHistoryDate(value: string | null | undefined): string {
  const parsed = Date.parse(clean(value))
  if (!Number.isFinite(parsed)) return ''
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(parsed))
}

export function formatHistoryAge(value: string | null | undefined, now = Date.now()): string {
  const parsed = Date.parse(clean(value))
  if (!Number.isFinite(parsed)) return ''
  const elapsed = Math.max(0, now - parsed)
  const minutes = Math.floor(elapsed / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months}mo ago`
  return `${Math.floor(months / 12)}y ago`
}
