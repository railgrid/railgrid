import { describe, expect, it } from 'vitest'
import AgentApprovalCard from '../components/AgentApprovalCard.vue'
import { mountVue, settleVue, text } from './vue-helper'

describe('permission approval card', () => {
  it('shows the command and directory as text and retains the full disclosed request', async () => {
    const args = JSON.stringify({ command: 'printf "<script>hello</script>"', cwd: '/workspace', reason: 'Print a marker', additionalPermissions: { network: false } })
    const decisions: string[] = []
    const view = await mountVue(AgentApprovalCard, { tool: 'Bash', args, onDecide: (decision: string) => decisions.push(decision) })
    expect(text(view.element)).toContain('Waiting for your approval')
    expect(text(view.element.querySelector('.agents-permission-card__command'))).toBe('printf "<script>hello</script>"')
    expect(text(view.element)).toContain('/workspace')
    expect(view.element.querySelector('script')).toBeNull()
    expect(text(view.element.querySelector('.agents-approval-args'))).toContain('additionalPermissions')
    view.element.querySelector<HTMLButtonElement>('.k-btn--primary')!.click()
    await settleVue()
    expect(decisions).toEqual(['approve'])
  })

  it.each(['', '{', '[]', 'null'])('blocks incomplete disclosure (%s) while allowing denial', async args => {
    const decisions: string[] = []
    const view = await mountVue(AgentApprovalCard, { tool: 'Bash', args, onDecide: (decision: string) => decisions.push(decision) })
    const approve = view.element.querySelector<HTMLButtonElement>('.k-btn--primary')!
    const deny = view.element.querySelector<HTMLButtonElement>('.secondary')!
    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(false)
    expect(text(view.element.querySelector('[role="alert"]'))).toContain('unavailable or malformed')
    approve.click()
    expect(decisions).toEqual([])
    deny.click()
    expect(decisions).toEqual(['deny'])
  })

  it('locks both choices during submission and shows a resolved receipt without actions', async () => {
    const view = await mountVue(AgentApprovalCard, { tool: 'notify', args: '{}', busy: 'approve' })
    expect([...view.element.querySelectorAll<HTMLButtonElement>('button')].every(button => button.disabled)).toBe(true)
    expect(text(view.element)).toContain('Approving…')
    expect(view.element.querySelector('.k-btn--primary')?.getAttribute('aria-busy')).toBe('true')
    await view.setProps({ busy: undefined, resolved: 'approve' })
    expect(view.element.querySelector('button')).toBeNull()
    expect(text(view.element.querySelector('[role="status"]'))).toContain('resuming')
  })
})
