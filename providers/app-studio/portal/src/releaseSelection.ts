import type { ProjectRelease } from './types'

function clean(value: string | null | undefined): string {
  return typeof value === 'string' ? value.trim() : ''
}

function releaseTime(release: ProjectRelease): number {
  for (const value of [release.completedAt, release.createdAt]) {
    const parsed = value ? Date.parse(value) : Number.NaN
    if (Number.isFinite(parsed)) return parsed
  }
  return 0
}

export function releaseHasPromotionEvidence(
  release: ProjectRelease | null | undefined,
): release is ProjectRelease & { releaseID: string } {
  return Boolean(release?.deployable && clean(release.commitSHA) && clean(release.releaseID))
}

/** Keep the newest release first without changing the server order of ties. */
export function orderReleases(releases: readonly ProjectRelease[]): ProjectRelease[] {
  return releases
    .map((release, index) => ({ release, index }))
    .sort((left, right) => releaseTime(right.release) - releaseTime(left.release) || left.index - right.index)
    .map(({ release }) => release)
}

export function newestDeployableRelease(releases: readonly ProjectRelease[]): ProjectRelease | null {
  return orderReleases(releases).find(releaseHasPromotionEvidence) ?? null
}
