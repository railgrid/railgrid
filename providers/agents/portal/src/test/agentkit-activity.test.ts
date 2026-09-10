import { defineComponent, h, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import AIActivityFeed from '../agentkit/AIActivityFeed.vue'
import type { AIActivityGroup, AIActivityRow } from '../agentkit/activity'
import { mountVue, settleVue, text } from './vue-helper'

const action = (id: string, title: string, overrides: Partial<AIActivityRow> = {}): AIActivityRow => ({
  id,
  title,
  status: 'succeeded',
  statusLabel: 'Completed',
  ...overrides,
})

function rowsIn(view: { element: HTMLElement }): HTMLElement[] {
  return [...view.element.querySelectorAll<HTMLElement>('.k-ai-action-row')]
}

describe('AgentKit activity feed presentation', () => {
  it('renders empty-label groups as flat activity without a blank heading', async () => {
    const view = await mountVue(AIActivityFeed, {
      panelId: 'activity-flat',
      expanded: true,
      summary: { count: 2, text: 'Completed' },
      groups: [{
        key: 'flat',
        label: '',
        rows: [action('flat-a', 'Read the file'), action('flat-b', 'Write the file')],
      } satisfies AIActivityGroup],
    })

    expect(view.element.querySelector('.k-ai-activity-feed__group-toggle')).toBeNull()
    expect(view.element.querySelector('.k-ai-activity-feed__group-label')).toBeNull()
    expect(view.element.querySelector('.k-ai-activity-feed__group-rows--ungrouped')).not.toBeNull()
    expect(rowsIn(view).map(row => row.id)).toEqual(['flat-a', 'flat-b'])
    expect(text(view.element)).toContain('2 actions · Completed')
  })

  it('keeps controlled row expansion attached to row identity after reorder', async () => {
    const rowA = action('row-a', 'Read the file', { expandable: true })
    const rowB = action('row-b', 'Write the file', {
      expandable: true,
      status: 'failed',
      statusLabel: 'Failed',
      error: true,
    })
    const groups = ref<readonly AIActivityGroup[]>([{
      key: 'ordered',
      label: 'Actions',
      rows: [rowA, rowB],
    }])

    const view = await mountVue(defineComponent({
      setup: () => () => h(AIActivityFeed, {
        panelId: 'activity-order',
        groups: groups.value,
        expanded: true,
        expandedRowKeys: ['row-b'],
      }, {
        details: ({ row }: { row: AIActivityRow }) => h('span', { class: 'row-detail' }, `Details for ${row.id}: ${row.title}`),
      }),
    }), {})

    expect(rowsIn(view).map(row => row.id)).toEqual(['row-a', 'row-b'])
    expect(rowsIn(view)[0]?.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')?.getAttribute('aria-expanded')).toBe('false')
    expect(rowsIn(view)[1]?.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')?.getAttribute('aria-expanded')).toBe('true')
    expect(text(view.element.querySelector('#activity-order-row-b-details'))).toBe('Details for row-b: Write the file')
    expect(view.element.querySelector('#activity-order-row-a-details')).toBeNull()

    groups.value = [{
      key: 'ordered',
      label: 'Actions',
      rows: [rowB, rowA],
    }]
    await settleVue()

    expect(rowsIn(view).map(row => row.id)).toEqual(['row-b', 'row-a'])
    expect(rowsIn(view)[0]?.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')?.getAttribute('aria-expanded')).toBe('true')
    expect(text(rowsIn(view)[0]?.querySelector('.k-ai-action-row__details'))).toBe('Details for row-b: Write the file')
  })

  it('collapses groups and marks long groups as scrollable', async () => {
    const bulkRows = Array.from({ length: 7 }, (_, index) => action(`bulk-${index + 1}`, `Step ${index + 1}`))
    const explicitRows = Array.from({ length: 7 }, (_, index) => action(`explicit-${index + 1}`, `Explicit step ${index + 1}`))
    const view = await mountVue(AIActivityFeed, {
      panelId: 'activity-groups',
      expanded: true,
      groups: [
        { key: 'research', label: 'Research', rows: bulkRows },
        { key: 'follow-up', label: 'Follow-up', rows: [action('follow-up-1', 'Summarize findings')] },
        { key: 'explicit', label: 'Explicitly unbounded', rows: explicitRows, scrollable: false },
      ] satisfies AIActivityGroup[],
    })

    const groupToggles = [...view.element.querySelectorAll<HTMLButtonElement>('.k-ai-activity-feed__group-toggle')]
    expect(groupToggles).toHaveLength(3)
    expect(groupToggles[0]?.getAttribute('aria-expanded')).toBe('true')
    expect(groupToggles[0]?.getAttribute('aria-controls')).toBe('activity-groups-research')
    expect(view.element.querySelector('#activity-groups-research.k-ai-activity-feed__group-rows--scrollable')).not.toBeNull()
    expect(view.element.querySelectorAll('#activity-groups-research .k-ai-action-row')).toHaveLength(7)
    expect(view.element.querySelector('#activity-groups-explicit.k-ai-activity-feed__group-rows--scrollable')).toBeNull()

    groupToggles[0]?.click()
    await settleVue()

    expect(groupToggles[0]?.getAttribute('aria-expanded')).toBe('false')
    expect(view.element.querySelector<HTMLElement>('#activity-groups-research')?.style.display).toBe('none')
    expect(view.element.querySelector<HTMLElement>('#activity-groups-follow-up')?.style.display).not.toBe('none')
    expect(view.element.querySelector<HTMLElement>('#activity-groups-explicit')?.style.display).not.toBe('none')
  })

  it('keeps unknown, error, and attention states truthful in row and summary evidence', async () => {
    const view = await mountVue(AIActivityFeed, {
      panelId: 'activity-states',
      expanded: true,
      summary: { count: 3, text: 'Needs attention', attention: true },
      groups: [{
        key: 'states',
        label: '',
        rows: [
          { id: 'unknown', title: 'Future provider step', status: 'future_state' },
          action('failed', 'Failed provider step', { status: 'failed', statusLabel: 'Failed', error: true }),
          action('waiting', 'Approval step', { status: 'waiting', statusLabel: 'Awaiting approval', attention: true }),
        ],
      } satisfies AIActivityGroup],
    })

    const unknown = view.element.querySelector<HTMLElement>('#unknown')!
    const failed = view.element.querySelector<HTMLElement>('#failed')!
    const waiting = view.element.querySelector<HTMLElement>('#waiting')!
    expect(text(unknown.querySelector('.sr-only'))).toBe('future_state:')
    expect(unknown.classList.contains('k-ai-action-row--error')).toBe(false)
    expect(unknown.classList.contains('k-ai-action-row--attention')).toBe(false)
    expect(failed.classList.contains('k-ai-action-row--error')).toBe(true)
    expect(text(failed.querySelector('.sr-only'))).toBe('Failed:')
    expect(waiting.classList.contains('k-ai-action-row--attention')).toBe(true)
    expect(text(waiting.querySelector('.sr-only'))).toBe('Awaiting approval:')

    const trigger = view.element.querySelector<HTMLButtonElement>('.k-ai-activity__trigger')!
    expect(trigger.querySelector('.k-ai-activity__status--attention')).not.toBeNull()
    expect(trigger.querySelector('.k-ai-activity__status--error')).toBeNull()
    expect(text(trigger)).toContain('3 actions · Needs attention')
  })
})
