import {
  nextSplitCreateMenuIndex,
  splitCreateLabel,
  splitCreateMenuActions,
  splitCreateMenuDismissesOnKey,
  splitCreatePrimaryAction,
} from './splitCreate.js'

function assert(condition: unknown, label: string): asserts condition {
  if (!condition) throw new Error(label)
}

assert(splitCreateLabel('warehouse') === 'New warehouse', 'warehouse primary label')
assert(splitCreateLabel('table') === 'New table', 'table primary label')
assert(splitCreatePrimaryAction() === 'browse', 'primary action opens the browse wizard')
assert(splitCreateMenuActions.join(',') === 'browse,manual', 'menu keeps Browse catalog before Enter manually')
assert(splitCreateMenuDismissesOnKey('Escape'), 'Escape dismisses the menu')
assert(splitCreateMenuDismissesOnKey('Tab'), 'Tab dismisses the menu without trapping focus')
assert(!splitCreateMenuDismissesOnKey('ArrowDown'), 'navigation keys keep the menu open')
assert(nextSplitCreateMenuIndex('ArrowDown', 0, 2) === 1, 'ArrowDown moves to the next menu item')
assert(nextSplitCreateMenuIndex('ArrowDown', 1, 2) === 0, 'ArrowDown wraps to the first item')
assert(nextSplitCreateMenuIndex('ArrowUp', 0, 2) === 1, 'ArrowUp wraps to the last item')
assert(nextSplitCreateMenuIndex('Home', 1, 2) === 0, 'Home focuses the first item')
assert(nextSplitCreateMenuIndex('End', 0, 2) === 1, 'End focuses the last item')
assert(nextSplitCreateMenuIndex('PageDown', 0, 2) === null, 'unhandled keys do not move menu focus')
