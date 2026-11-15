import type { RepositoryDraft } from './repositoryDraft'

export interface RepositoryAttempt {
  mode: 'create' | 'import'
  draft: RepositoryDraft
  resourceName: string
  remote: string
}

// A transport error, server failure, timeout or malformed success may follow a
// committed write. Only an explicit client rejection settles the outcome.
export function repositoryCreateRejected(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const value = error as { reason?: string; message?: string }
  if (value.reason !== 'HTTPError') return false
  const status = Number(value.message?.match(/^(\d{3}):/)?.[1])
  return status >= 400 && status < 500 && status !== 408 && status !== 429
}

export function repositoryAttemptKey(scope: string): string {
  return `railgrid:code:repository-attempt:${scope}`
}

export function readRepositoryAttempt(storage: Storage, scope: string): RepositoryAttempt | null {
  const raw = storage.getItem(repositoryAttemptKey(scope))
  if (!raw) return null
  const value: unknown = JSON.parse(raw)
  if (!value || typeof value !== 'object') throw new Error('Invalid repository recovery record')
  const attempt = value as RepositoryAttempt
  if ((attempt.mode !== 'create' && attempt.mode !== 'import') || typeof attempt.resourceName !== 'string' || typeof attempt.remote !== 'string' || !attempt.draft || typeof attempt.draft.name !== 'string' || typeof attempt.draft.repo !== 'string' || typeof attempt.draft.connectionRef !== 'string' || typeof attempt.draft.visibility !== 'string' || typeof attempt.draft.description !== 'string' || typeof attempt.draft.autoInit !== 'boolean') throw new Error('Invalid repository recovery record')
  return attempt
}

// Bounds the whole operation, including response-body decoding. The transport
// may still finish later; callers must treat a timeout as an unknown outcome.
export async function boundedRepositoryRequest<T>(request: Promise<T>, timeoutMs = 15000): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      request,
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error('The request timed out.')), timeoutMs) }),
    ])
  } finally {
    if (timer !== undefined) clearTimeout(timer)
  }
}
