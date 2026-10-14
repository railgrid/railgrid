// One short-lived, in-memory handoff between the floating and routed surfaces.
// Never persist drafts to browser storage or share them across identities.
let pending: { key: string; draft: string; expires: number } | null = null
export function offerChatDraft(key: string, draft: string): void {
  pending = { key, draft, expires: Date.now() + 30_000 }
}
export function takeChatDraft(key: () => string): string | undefined {
  if (!pending) return undefined
  const value = pending
  pending = null
  return value.key === key() && value.expires > Date.now() ? value.draft : undefined
}
