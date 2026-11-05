// CANONICAL SOURCE — provider-sdk/portalkit-vue. Run `make sync-portalkit` after editing.
import { shallowRef, watch } from 'vue'
import { providerFetch, type ProviderFetchContext } from '../portalkit/tenant'

interface UserLabelIdentity {
  userId?: string
  sub?: string
  email?: string
}

interface UserLabelContext extends ProviderFetchContext {
  orgUUID?: string | null
  workspaceUUID?: string | null
  user?: UserLabelIdentity | null
}

interface MemberIdentity {
  user?: string
  rbacIdentity?: string
  email?: string
  userDisplayName?: string
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function aliases(member: MemberIdentity): string[] {
  const email = text(member.email)
  return [text(member.user), text(member.rbacIdentity), email, email ? `railgrid:${email}` : ''].filter(Boolean)
}

/** Match both provider actor IDs and kcp principals to the same account label. */
export function resolveUserLabel(actorID: unknown, user?: UserLabelIdentity | null, members: readonly MemberIdentity[] = []): string {
  const actor = text(actorID)
  if (!actor) return ''
  const ownAliases = [text(user?.userId), text(user?.sub), text(user?.email), user?.email ? `railgrid:${text(user.email)}` : ''].filter(Boolean)
  const member = members.find(candidate => aliases(candidate).includes(actor))
  if (ownAliases.includes(actor) || (member && aliases(member).some(alias => ownAliases.includes(alias)))) {
    // Use the same email-first choice as locally submitted messages, even if
    // membership metadata has not loaded yet or cannot be read by this caller.
    return text(user?.email) || text(member?.email) || text(member?.userDisplayName) || text(user?.userId) || text(user?.sub) || actor
  }
  return text(member?.email) || text(member?.userDisplayName) || text(member?.user) || actor
}

/** Read the existing, caller-authorized member lists once per conversation scope. */
export function useUserLabels(getContext: () => UserLabelContext | null | undefined) {
  const members = shallowRef<MemberIdentity[]>([])
  let activeScope = ''
  watch(() => {
    const ctx = getContext()
    return [ctx?.fetch, ctx?.fetch ? null : ctx?.token, ctx?.orgUUID, ctx?.workspaceUUID, ctx?.user?.userId, ctx?.user?.sub, ctx?.user?.email] as const
  }, async (_current, _previous, onCleanup) => {
    const ctx = getContext()
    const scope = JSON.stringify([ctx?.orgUUID, ctx?.workspaceUUID, ctx?.user])
    if (scope !== activeScope) {
      activeScope = scope
      members.value = []
    }
    if (!ctx?.orgUUID || (!ctx.fetch && !ctx.token)) return
    const controller = new AbortController()
    onCleanup(() => controller.abort())
    const org = encodeURIComponent(ctx.orgUUID)
    const paths = [`/api/orgs/${org}/memberships`]
    if (ctx.workspaceUUID) paths.push(`/api/orgs/${org}/workspaces/${encodeURIComponent(ctx.workspaceUUID)}/memberships`)
    const results = await Promise.allSettled(paths.map(async path => {
      const response = await providerFetch(ctx)(path, { signal: controller.signal })
      if (!response.ok) return []
      const body = await response.json()
      return Array.isArray(body?.items) ? body.items.filter((item: unknown) => item && typeof item === 'object') as MemberIdentity[] : []
    }))
    if (controller.signal.aborted) return
    members.value = results.flatMap(result => result.status === 'fulfilled' ? result.value : [])
  }, { immediate: true })
  return { userLabel: (actorID: unknown) => resolveUserLabel(actorID, getContext()?.user, members.value) }
}
