import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { resolveUserLabel, useUserLabels } from '../portalkit/useUserLabels'

const alice = { user: 'user-alice', rbacIdentity: 'railgrid:alice@example.com', email: 'alice@example.com', userDisplayName: 'Alice' }
const bob = { user: 'user-bob', rbacIdentity: 'railgrid:bob@example.com', email: 'bob@example.com', userDisplayName: 'Bob' }

async function settle() {
  for (let index = 0; index < 8; index++) { await Promise.resolve(); await nextTick() }
}

describe('conversation user labels', () => {
  it('keeps pending email, MCP user ID and kcp principal identical for the current account', () => {
    const user = { userId: alice.user, email: alice.email }
    for (const actor of [alice.email, alice.user, alice.rbacIdentity]) {
      expect(resolveUserLabel(actor, user)).toBe(alice.email)
      expect(resolveUserLabel(actor, user, [alice])).toBe(alice.email)
    }
  })

  it('resolves another member consistently across both transports and accounts without email', () => {
    for (const actor of [bob.user, bob.rbacIdentity]) expect(resolveUserLabel(actor, { userId: alice.user }, [bob])).toBe(bob.email)
    const tokenUser = { user: 'static-user-test', rbacIdentity: 'railgrid:static:test' }
    expect(resolveUserLabel(tokenUser.user, null, [tokenUser])).toBe(tokenUser.user)
    expect(resolveUserLabel(tokenUser.rbacIdentity, null, [tokenUser])).toBe(tokenUser.user)
    expect(resolveUserLabel('user-departed', null, [bob])).toBe('user-departed')
    expect(resolveUserLabel(undefined)).toBe('')
  })

  it('loads both authorized member lists through the host transport without reading its token', async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({ items: [alice, bob] })))
    const context = { fetch, orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: alice.user, email: alice.email }, get token(): never { throw new Error('Deprecated token read') } }
    const scope = effectScope()
    const labels = scope.run(() => useUserLabels(() => context))!
    try {
      expect(labels.userLabel(alice.user)).toBe(alice.email)
      await settle()
      expect(fetch.mock.calls).toHaveLength(2)
      expect(labels.userLabel(bob.user)).toBe(bob.email)
      expect(labels.userLabel(bob.rbacIdentity)).toBe(bob.email)
    } finally { scope.stop() }
  })

  it('clears old membership data and ignores a late response after changing workspace', async () => {
    let release!: (response: Response) => void
    const slowFetch = vi.fn(() => new Promise<Response>(resolve => { release = resolve }))
    const context = ref({ fetch: slowFetch, orgUUID: 'org-a', user: { userId: alice.user, email: alice.email } })
    const scope = effectScope()
    const labels = scope.run(() => useUserLabels(() => context.value))!
    try {
      context.value = { fetch: vi.fn(async () => new Response(JSON.stringify({ items: [] }))), orgUUID: 'org-b', user: { userId: 'other-user', email: 'other@example.com' } }
      await nextTick()
      release(new Response(JSON.stringify({ items: [alice, bob] })))
      await settle()
      expect(labels.userLabel(bob.user)).toBe(bob.user)
      expect(labels.userLabel(alice.user)).toBe(alice.user)
    } finally { scope.stop() }
  })

  it('keeps the current account readable when member lists are forbidden or unavailable', async () => {
    const scope = effectScope()
    const labels = scope.run(() => useUserLabels(() => ({ fetch: async () => new Response('', { status: 403 }), orgUUID: 'org', user: { userId: alice.user, email: alice.email } })))!
    try {
      await settle()
      expect(labels.userLabel(alice.user)).toBe(alice.email)
      expect(labels.userLabel(alice.rbacIdentity)).toBe(alice.email)
      expect(labels.userLabel(bob.user)).toBe(bob.user)
    } finally { scope.stop() }
  })
})
