import { describe, expect, it } from 'vitest'
import AITranscriptActivity from '../agentkit/AITranscriptActivity.vue'
import { activitySummaryTitle, commandActivityPresentation, type AIActivityRow } from '../agentkit/activity'
import { mountVue, settleVue, text } from './vue-helper'

const row = (id: string, iconKey = 'run'): AIActivityRow => ({ id, title: `Ran ${id}`, iconKey, status: 'succeeded', expandable: true })
const groupButton = (el: HTMLElement) => el.querySelector<HTMLButtonElement>('.k-ai-transcript-activity__toggle')!

describe('transcript activity', () => {
  it('renders one action directly and preserves expanded output as a second action arrives', async () => {
    const first = row('pwd')
    const view = await mountVue(AITranscriptActivity, { panelId: 'activity', rows: [first] })
    expect(groupButton(view.element)).toBeNull()
    expect(view.element.querySelector('.k-ai-action-row__toggle')?.getAttribute('aria-expanded')).toBe('false')
    view.element.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settleVue()
    await view.setProps({ rows: [{ ...first }, row('pwd-again')] })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('true')
    expect(view.element.querySelector('.k-ai-action-row__toggle')?.getAttribute('aria-expanded')).toBe('true')
    groupButton(view.element).click()
    await settleVue()
    await view.setProps({ rows: [{ ...first }, { ...row('pwd-again'), busy: true, status: 'running' }] })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('false')
    groupButton(view.element).click()
    await settleVue()
    expect(view.element.querySelector('.k-ai-action-row__toggle')?.getAttribute('aria-expanded')).toBe('true')
  })

  it('uses one semantic group, keeps every call in order, and preserves user choices across phase updates', async () => {
    const rows = [row('read-a', 'read'), row('edit', 'edit'), row('pwd'), row('read-b', 'search')]
    const view = await mountVue(AITranscriptActivity, { panelId: 'mixed', rows })
    expect(text(groupButton(view.element))).toBe('Edited files, read files, ran commands · 4 calls')
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('false')
    expect(view.element.querySelectorAll('.k-ai-transcript-activity__toggle')).toHaveLength(1)
    groupButton(view.element).click()
    await settleVue()
    expect([...view.element.querySelectorAll('.k-ai-action-row')].map(el => el.id)).toEqual(rows.map(row => row.id))
    await view.setProps({ rows: rows.map(row => ({ ...row, status: 'running', busy: true })) })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('true')
    expect(text(groupButton(view.element))).toBe('Editing files, reading files, running commands · 4 calls')
    await view.setProps({ rows })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('true')
  })

  it('shows active calls before completion and respects a manual collapse during later calls', async () => {
    const first = row('finished')
    const running = { ...row('slow'), status: 'running', busy: true }
    const view = await mountVue(AITranscriptActivity, { panelId: 'live', rows: [first, running] })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('true')
    expect(view.element.querySelector<HTMLElement>('.k-ai-transcript-activity__list')?.style.display).not.toBe('none')
    expect(view.element.querySelector('.k-ai-action-row--busy')?.textContent).toContain('slow')
    groupButton(view.element).click()
    await settleVue()
    await view.setProps({ rows: [first, row('slow'), { ...row('next'), status: 'running', busy: true }] })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('false')
    groupButton(view.element).click()
    await settleVue()
    await view.setProps({ rows: [first, row('slow'), row('next')] })
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('true')
  })

  it('labels preview inspection groups independently of the first call and retains all calls', async () => {
    const rows = ['Inspected preview', 'Captured preview screenshot', 'Captured preview screenshot'].map((title, i) => ({ ...row(`preview-${i}`, 'inspect'), category: 'inspect' as const, title, expandable: false }))
    const view = await mountVue(AITranscriptActivity, { panelId: 'preview', rows })
    expect(text(groupButton(view.element))).toBe('Inspected resources · 3 calls')
    groupButton(view.element).click()
    await settleVue()
    expect([...view.element.querySelectorAll('.k-ai-action-row__title')].map(el => el.textContent)).toEqual(rows.map(row => row.title))
    expect(view.element.querySelectorAll('.k-ai-action-row')).toHaveLength(3)
    expect(view.element.querySelectorAll('.k-ai-action-row__toggle')).toHaveLength(0)
    await view.setProps({ rows: rows.map(row => ({ ...row, busy: true, status: 'running' })) })
    expect(text(groupButton(view.element))).toBe('Inspecting resources · 3 calls')
    groupButton(view.element).click()
    await settleVue()
    expect(groupButton(view.element).getAttribute('aria-expanded')).toBe('false')
  })

  it('bounds long call lists, exposes all activities, and removes the bound while a row is expanded', async () => {
    const view = await mountVue(AITranscriptActivity, { panelId: 'long', rows: Array.from({ length: 12 }, (_, i) => row(`cmd-${i}`)) })
    groupButton(view.element).click()
    await settleVue()
    expect(view.element.querySelector('.k-ai-transcript-activity__list')?.classList.contains('is-bounded')).toBe(true)
    view.element.querySelector<HTMLButtonElement>('.k-ai-transcript-activity__more')!.click()
    await settleVue()
    expect(view.element.querySelector('.k-ai-transcript-activity__list')?.classList.contains('is-bounded')).toBe(false)
    view.element.querySelector<HTMLButtonElement>('.k-ai-transcript-activity__more')!.click()
    view.element.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settleVue()
    expect(view.element.querySelector('.k-ai-transcript-activity__list')?.classList.contains('is-bounded')).toBe(false)
  })

  it('keeps shell-separated scripts and unexpanded shell patterns opaque', () => {
    for (const command of ['ls\nrm x', 'cat a\nrm b', 'ls\r\nrm x', 'cat a\rrm b', "cat 'a\nb'", 'ls *.go', 'ls src/?.ts', 'ls [ab].ts', 'ls ~', 'cat ~/notes', 'cat {a,b}']) {
      expect(commandActivityPresentation(command)).toEqual({
        title: `Ran ${command}`, runningTitle: `Running ${command}`, category: 'run', iconKey: 'run',
      })
    }
    expect(commandActivityPresentation("ls '*.go'").title).toBe('Listed files in *.go')
    expect(commandActivityPresentation("cat '~'").title).toBe('Read ~')
    expect(commandActivityPresentation('opaque', ['cat', 'a\nb']).title).toBe('Read a\nb')
  })

  it('describes only unambiguous simple commands and preserves compound invocations', () => {
    expect(commandActivityPresentation('ls provider-sdk').title).toBe('Listed files in provider-sdk')
    expect(commandActivityPresentation("cat 'docs/read me.md'").title).toBe('Read docs/read me.md')
    expect(commandActivityPresentation("rg 'copy|clipboard' provider-sdk").title).toBe('Searched for copy|clipboard in provider-sdk')
    for (const command of ['cat a; cat b', 'ls && pwd', 'ls $(pwd)', 'ls | cat', "ls'path'", 'ls --ignore path']) {
      expect(commandActivityPresentation(command).title).toBe(`Ran ${command}`)
      expect(commandActivityPresentation(command).category).toBe('run')
    }
    expect(commandActivityPresentation('opaque', ['rg', '-e', 'copy|clipboard', 'App.vue']).iconKey).toBe('search')
    expect(activitySummaryTitle([{ ...row('unverified'), status: 'unknown' }])).toBe('Commands')
    expect(activitySummaryTitle([{ ...row('blocked'), status: 'blocked', attention: true }])).toBe('Commands')
  })
})
