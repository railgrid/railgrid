import { afterEach, describe, expect, it, vi } from 'vitest'
import { containModalTab } from '../../provider-sdk/portalkit/modal-focus'

function fixture() {
  const dialog = document.createElement('dialog')
  dialog.innerHTML = '<input><fieldset disabled><button>Inherited disabled</button></fieldset><button style="visibility:hidden">Invisible</button><button disabled>Disabled</button><button hidden>Hidden</button><button>Cancel</button>'
  document.body.append(dialog)
  dialog.open = true
  for (const element of dialog.querySelectorAll<HTMLElement>('input, button')) {
    vi.spyOn(element, 'getClientRects').mockReturnValue(element.hidden ? [] as unknown as DOMRectList : [new DOMRect(0, 0, 10, 10)] as unknown as DOMRectList)
  }
  dialog.addEventListener('keydown', containModalTab)
  return dialog
}
afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks() })

describe('native modal Tab boundary', () => {
  it('wraps visible enabled controls in both directions and leaves IME keys alone', () => {
    const dialog = fixture()
    const input = dialog.querySelector('input')!
    const cancel = [...dialog.querySelectorAll('button')].at(-1)!
    input.focus()
    const previous = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true })
    input.dispatchEvent(previous)
    expect(previous.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(cancel)
    const next = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    cancel.dispatchEvent(next)
    expect(document.activeElement).toBe(input)
    const composition = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, isComposing: true, bubbles: true, cancelable: true })
    input.dispatchEvent(composition)
    expect(composition.defaultPrevented).toBe(false)
    expect(document.activeElement).toBe(input)
  })
  it('does not intercept a nested dialog or a prevented popup event', () => {
    const outer = fixture()
    const nested = document.createElement('dialog')
    nested.open = true
    nested.innerHTML = '<button>Nested</button>'
    outer.append(nested)
    const event = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    nested.querySelector('button')!.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
    const popupEvent = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    popupEvent.preventDefault()
    outer.querySelector('input')!.dispatchEvent(popupEvent)
    expect(popupEvent.defaultPrevented).toBe(true)
  })
})
