import { describe, expect, it, vi } from 'vitest'
import SessionNotifications from '../components/SessionNotifications.vue'
import type { SessionNotification } from '../types'
import { mountVue, settleVue, text } from './vue-helper'

const notice: SessionNotification = {
  id: 'n1', sessionID: 's1', key: 'build', producer: 'agent-uid', payload: 'Build complete', createdAt: '2026-11-12T10:00:00Z',
}

describe('session notifications panel', () => {
  it('loads a separate read-only feed for the selected session and refreshes on session change', async () => {
    const listSessionNotifications = vi.fn(async (_agent: string, session: string) => session === 's1' ? [notice] : [])
    const view = await mountVue(SessionNotifications, { api: { listSessionNotifications }, agent: 'builder', session: 's1' })
    try {
      await settleVue()
      expect(listSessionNotifications).toHaveBeenCalledWith('builder', 's1')
      expect(text(view.element.querySelector('.agents-session-notifications__list'))).toContain('Build complete')
      expect(view.element.querySelector('[aria-label="Conversation transcript"]')).toBeNull()
      await view.setProps({ session: 's2' })
      expect(listSessionNotifications).toHaveBeenLastCalledWith('builder', 's2')
      expect(text(view.element.querySelector('.agents-session-notifications'))).toContain('No notifications for this session.')
      expect(text(view.element.querySelector('.agents-session-notifications'))).not.toContain('Build complete')
    } finally {
      view.unmount()
    }
  })

  it('shows a retryable error separately from an empty feed', async () => {
    const listSessionNotifications = vi.fn().mockRejectedValueOnce(new Error('forbidden')).mockResolvedValueOnce([])
    const view = await mountVue(SessionNotifications, { api: { listSessionNotifications }, agent: 'builder', session: 's1' })
    try {
      await settleVue()
      expect(text(view.element.querySelector('[role="alert"]'))).toContain('Could not load session notifications. forbidden')
      view.element.querySelector<HTMLButtonElement>('[role="alert"] button')!.click()
      await settleVue()
      expect(text(view.element.querySelector('[role="status"]'))).toContain('No notifications for this session.')
    } finally {
      view.unmount()
    }
  })

  it('does not display an older session response after selection changes', async () => {
    let finishOld!: (items: SessionNotification[]) => void
    const oldResponse = new Promise<SessionNotification[]>(resolve => { finishOld = resolve })
    const listSessionNotifications = vi.fn((_agent: string, session: string) => session === 's1' ? oldResponse : Promise.resolve([]))
    const view = await mountVue(SessionNotifications, { api: { listSessionNotifications }, agent: 'builder', session: 's1' })
    try {
      await view.setProps({ session: 's2' })
      finishOld([notice])
      await settleVue()
      expect(text(view.element.querySelector('.agents-session-notifications'))).toContain('No notifications for this session.')
      expect(text(view.element.querySelector('.agents-session-notifications'))).not.toContain('Build complete')
    } finally {
      view.unmount()
    }
  })
})
