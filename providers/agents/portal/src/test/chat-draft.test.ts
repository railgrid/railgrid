import { describe, expect, it } from 'vitest'
import { agentChatDraftStorageKey, readAgentChatDraft, writeAgentChatDraft } from '../chatDraft'

describe('agent chat draft ownership', () => {
  it('scopes durable drafts to the actor, agent, and session', () => {
    const alice = { usable: true, tenantKey: 'org/workspace', userKey: 'alice', token: 'token-a' }
    const bob = { ...alice, userKey: 'bob', token: 'token-b' }
    const aliceSession = agentChatDraftStorageKey(alice, 'reviewer', 'session-a')
    const otherSession = agentChatDraftStorageKey(alice, 'reviewer', 'session-b')
    const otherActor = agentChatDraftStorageKey(bob, 'reviewer', 'session-a')

    expect(aliceSession).toBeTruthy()
    expect(new Set([aliceSession, otherSession, otherActor]).size).toBe(3)
    expect(writeAgentChatDraft(aliceSession, {
      content: 'keep this only for Alice in session A',
      contentParts: [],
      skills: [],
      resources: [],
    })).toBe(true)
    expect(readAgentChatDraft(aliceSession).content).toBe('keep this only for Alice in session A')
    expect(readAgentChatDraft(otherSession).content).toBe('')
    expect(readAgentChatDraft(otherActor).content).toBe('')

    localStorage.removeItem(aliceSession)
  })
})
