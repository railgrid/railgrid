interface ChatModelAuthority {
  tenantKey: string
  userKey: string | null
}

const pendingByScope = new Map<string, Promise<void>>()

/**
 * Scope model preference requests to one authority, agent, and exact chat.
 * The queue is shared across component mounts. The request's captured runtime
 * authority is still checked immediately before it starts; omitting the
 * per-mount epoch here makes the same caller wait on writes from another view.
 */
export function chatModelRequestKey(
  authority: ChatModelAuthority,
  agent: string,
  session: string,
): string {
  return JSON.stringify([authority.tenantKey, authority.userKey, agent, session])
}

/**
 * Run reads and writes for one chat in call order. A queued request is skipped
 * if its view lost authority or stopped owning that chat before its turn.
 */
export async function serializeChatModelRequest<T>(
  key: string,
  isCurrent: () => boolean,
  request: () => Promise<T>,
): Promise<T | undefined> {
  const previous = pendingByScope.get(key)
  let release!: () => void
  const turn = new Promise<void>(resolve => { release = resolve })
  pendingByScope.set(key, turn)

  if (previous) await previous
  try {
    if (!isCurrent()) return undefined
    return await request()
  } finally {
    release()
    if (pendingByScope.get(key) === turn) pendingByScope.delete(key)
  }
}
