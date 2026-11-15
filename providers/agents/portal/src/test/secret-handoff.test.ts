import { describe, expect, it, vi } from 'vitest'
import SecretHandoff from '../components/SecretHandoff.vue'
import { mountVue, settleVue, text } from './vue-helper'

describe('one-time setup handoff', () => {
  it('offers explicit manual selection after clipboard failure and hides it again', async () => {
    const secret = 'https://example.test/slack/secret'
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    const view = await mountVue(SecretHandoff, { value: secret, label: 'Slack request URL' })
    expect(view.element.innerHTML).not.toContain(secret)
    expect(text(view.element)).not.toContain('Reveal value')
    view.element.querySelector<HTMLButtonElement>('button')!.click()
    await settleVue()
    expect(view.element.innerHTML).not.toContain(secret)
    expect(view.element.querySelector('[role="alert"]')?.textContent).toContain('Could not copy')
    const reveal = [...view.element.querySelectorAll('button')].find(b => text(b).startsWith('Reveal'))!
    reveal.click()
    await settleVue()
    const field = view.element.querySelector<HTMLTextAreaElement>('textarea')!
    expect(field.value).toBe(secret)
    expect(field.readOnly).toBe(true)
    expect(document.activeElement).toBe(field)
    expect(field.selectionEnd - field.selectionStart).toBe(secret.length)
    ;[...view.element.querySelectorAll('button')].find(b => text(b) === 'Hide value')!.click()
    await settleVue()
    expect(view.element.querySelector('textarea')).toBeNull()
    expect(view.element.innerHTML).not.toContain(secret)
    expect(document.activeElement?.textContent).toContain('Reveal value')
  })

  it.each(['recovery', 'outside'])('preserves focus on successful retry with %s focused', async target => {
    let resolve!: () => void
    const pending = new Promise<void>(done => { resolve = done })
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValueOnce(new Error('denied')).mockReturnValueOnce(pending)
    const view = await mountVue(SecretHandoff, { value: 'secret' })
    const copy = view.element.querySelector<HTMLButtonElement>('button')!
    copy.click()
    await settleVue()
    copy.click()
    ;[...view.element.querySelectorAll('button')].find(b => text(b).startsWith('Reveal'))!.click()
    await settleVue()
    if (target === 'outside') {
      const outside = document.createElement('button')
      document.body.appendChild(outside)
      outside.focus()
    }
    const previous = document.activeElement
    resolve()
    await settleVue()
    expect(document.activeElement).toBe(target === 'outside' ? previous : copy)
    expect(view.element.querySelector('textarea')).toBeNull()
  })

  it('ignores an older clipboard completion after a newer failure', async () => {
    let resolve!: () => void
    const first = new Promise<void>(done => { resolve = done })
    vi.spyOn(navigator.clipboard, 'writeText').mockReturnValueOnce(first).mockRejectedValueOnce(new Error('denied'))
    const view = await mountVue(SecretHandoff, { value: 'secret' })
    const copy = view.element.querySelector<HTMLButtonElement>('button')!
    copy.click()
    copy.click()
    await settleVue()
    resolve()
    await settleVue()
    expect(view.element.querySelector('[role="alert"]')).not.toBeNull()
    expect(text(view.element)).not.toContain('Copied')
  })

  it('remasks a revealed value when the value changes', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    const view = await mountVue(SecretHandoff, { value: 'old-secret' })
    view.element.querySelector<HTMLButtonElement>('button')!.click()
    await settleVue()
    ;[...view.element.querySelectorAll('button')].find(b => text(b).startsWith('Reveal'))!.click()
    await settleVue()
    expect(view.element.querySelector('textarea')?.value).toBe('old-secret')
    await view.setProps({ value: 'new-secret' })
    expect(view.element.querySelector('textarea')).toBeNull()
    expect(document.activeElement).toBe(view.element.querySelector('button'))
    expect(view.element.innerHTML).not.toContain('old-secret')
    expect(view.element.innerHTML).not.toContain('new-secret')
  })

  it.each(['reveal', 'hide', 'outside'])('preserves usable focus when value changes with %s focused', async target => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    const view = await mountVue(SecretHandoff, { value: 'old-secret' })
    const copy = view.element.querySelector<HTMLButtonElement>('button')!
    copy.click()
    await settleVue()
    const reveal = [...view.element.querySelectorAll('button')].find(b => text(b).startsWith('Reveal'))!
    if (target === 'hide') {
      reveal.click()
      await settleVue()
      ;[...view.element.querySelectorAll('button')].find(b => text(b) === 'Hide value')!.focus()
    } else if (target === 'reveal') reveal.focus()
    else {
      const outside = document.createElement('button')
      document.body.appendChild(outside)
      outside.focus()
    }
    const originalFocus = document.activeElement
    await view.setProps({ value: 'new-secret' })
    expect(document.activeElement).toBe(target === 'outside' ? originalFocus : copy)
  })

  it('clears disclosure and ignores late copy failures when the value changes', async () => {
    let reject!: (reason: Error) => void
    vi.spyOn(navigator.clipboard, 'writeText').mockReturnValue(new Promise((_, fail) => { reject = fail }))
    const view = await mountVue(SecretHandoff, { value: 'old-secret' })
    view.element.querySelector<HTMLButtonElement>('button')!.click()
    await view.setProps({ value: 'new-secret' })
    reject(new Error('denied'))
    await settleVue()
    expect(view.element.innerHTML).not.toContain('old-secret')
    expect(view.element.innerHTML).not.toContain('new-secret')
    expect(view.element.querySelector('[role="alert"]')).toBeNull()
    expect(text(view.element)).not.toContain('Reveal value')
  })
})
