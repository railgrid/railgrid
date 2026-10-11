import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { mountVue, settleVue, text } from './vue-helper'

// Behavioural check of the shared kit's cell truncation (the kit's own tests
// pin source text only). jsdom lays nothing out, so overflow is simulated:
// every truncating box reports a wider scroll width than its client width.
const LONG_TRIGGER = 'webhook:github-pull-request-review-requested-on-railgrid'
const LONG_NAME = 'You are a principal software engineer conducting a rigorous code review'

const widths: PropertyDescriptor[] = []
function simulateOverflow() {
  for (const [name, value] of [['scrollWidth', 400], ['clientWidth', 100]] as const) {
    widths.push(Object.getOwnPropertyDescriptor(HTMLElement.prototype, name) ?? { configurable: true, value: 0 })
    Object.defineProperty(HTMLElement.prototype, name, {
      configurable: true,
      get() {
        const el = this as HTMLElement
        return el.classList.contains('k-table__cell-value') || el.classList.contains('k-table__primary-value') ? value : 0
      },
    })
  }
}

async function mountTable(interactive = true) {
  const rowClick = vi.fn()
  const mounted = await mountVue(ResourceTable, {
    ariaLabel: 'Runs',
    variant: 'simple',
    loaded: true,
    interactive,
    columns: [
      { key: 'name', label: 'Run', primary: true },
      { key: 'trigger', label: 'Trigger' },
      { key: 'phase', label: 'Phase' },
    ],
    rows: [{ name: LONG_NAME, trigger: LONG_TRIGGER, phase: 'ok' }],
    onRowClick: rowClick,
  })
  await settleVue(2, 20)
  const row = mounted.element.querySelector<HTMLElement>('.k-table__row')!
  const cells = [...mounted.element.querySelectorAll<HTMLElement>('.k-table__cell-value')]
  return { mounted, rowClick, row, cells }
}

function tooltip(): HTMLElement | null {
  return document.body.querySelector<HTMLElement>('.k-table__primary-tooltip')
}

describe('ResourceTable cell truncation', () => {
  beforeEach(simulateOverflow)
  afterEach(() => {
    Object.defineProperty(HTMLElement.prototype, 'clientWidth', widths.pop()!)
    Object.defineProperty(HTMLElement.prototype, 'scrollWidth', widths.pop()!)
    widths.length = 0
  })

  it('wraps secondary values in a truncating box that is not a tab stop', async () => {
    const { cells } = await mountTable()
    expect(cells.map(cell => text(cell))).toEqual([LONG_TRIGGER, 'ok'])
    expect(cells.every(cell => !cell.hasAttribute('tabindex'))).toBe(true)
    expect(cells[0]!.dataset.fullValue).toBe(LONG_TRIGGER)
  })

  it('still opens the row when a long secondary cell is clicked or Enter is pressed on the row', async () => {
    const { rowClick, row, cells } = await mountTable()
    cells[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await settleVue()
    expect(rowClick).toHaveBeenCalledTimes(1)

    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settleVue()
    expect(rowClick).toHaveBeenCalledTimes(2)
  })

  it('discloses a truncated secondary cell on hover and from the focused row with the arrow keys', async () => {
    const { row, cells } = await mountTable()
    cells[0]!.dispatchEvent(new MouseEvent('mouseenter'))
    await settleVue(3, 20)
    expect(text(tooltip())).toBe(LONG_TRIGGER)
    cells[0]!.dispatchEvent(new MouseEvent('mouseleave'))
    await settleVue()
    expect(tooltip()).toBeNull()

    row.focus()
    row.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    await settleVue(3, 20)
    expect(text(tooltip())).toBe(LONG_NAME)

    const right = new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true })
    row.dispatchEvent(right)
    await settleVue(3, 20)
    expect(right.defaultPrevented).toBe(true)
    expect(text(tooltip())).toBe(LONG_TRIGGER)

    // The short phase cell also "overflows" under the simulation, so the
    // cycle is primary → trigger → phase → primary.
    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    await settleVue(3, 20)
    expect(text(tooltip())).toBe('ok')
    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }))
    await settleVue(3, 20)
    expect(text(tooltip())).toBe(LONG_TRIGGER)

    row.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    await settleVue()
    expect(tooltip()).toBeNull()
  })

  it('leaves the arrow keys alone when nothing in the row is truncated', async () => {
    Object.defineProperty(HTMLElement.prototype, 'scrollWidth', { configurable: true, get: () => 0 })
    const { row } = await mountTable()
    const right = new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true })
    row.dispatchEvent(right)
    await settleVue(3, 20)
    expect(right.defaultPrevented).toBe(false)
    expect(tooltip()).toBeNull()
  })
})
