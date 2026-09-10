import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const dock = fs.readFileSync(new URL('./TerminalDock.vue', import.meta.url), 'utf8')

test('terminal tab actions remain reachable on coarse and hybrid pointers', () => {
  assert.doesNotMatch(dock, /ref="dockRoot"/)
  assert.match(dock, /'terminal-tab group[\s\S]*sm:h-8'/)
  assert.match(dock, /class="terminal-tab-button[\s\S]*sm:min-h-8"/)
  assert.match(dock, /class="terminal-tab-action[\s\S]*sm:opacity-0[\s\S]*sm:group-focus-within:opacity-100"/)
  assert.match(dock, /@media \(pointer: coarse\), \(any-pointer: coarse\) \{[\s\S]*\.terminal-tab,[\s\S]*\.terminal-tab-button,[\s\S]*\.terminal-tab-action[\s\S]*min-height: 44px/)
  assert.match(dock, /\.terminal-tab-action \{[\s\S]*opacity: 1;[\s\S]*width: 44px;/)
})

test('terminal panel max height follows the reactive viewport value', () => {
  assert.match(dock, /const viewportHeight = ref\(typeof window === 'undefined' \? 160 : window\.innerHeight\)/)
  assert.match(dock, /const panelHeightMax = computed\(\(\) =>[\s\S]*viewportHeight\.value - 64/)
  assert.match(dock, /function onWindowResize\(\) \{[\s\S]*viewportHeight\.value = window\.innerHeight/)
  assert.match(dock, /:aria-valuemax="panelHeightMax"/)
})

test('terminal reorder does not trigger xterm focus from the session-order watcher', () => {
  assert.match(dock, /watch\(\s*\/\/ Reordering changes the array order[\s\S]*\(\) => store\.activeSessionId,[\s\S]*nextTick\(refocusActive\)/)
  assert.doesNotMatch(dock, /store\.sessions\.map\(\(s\) => s\.id\)\.join\(','\)/)
  const reorderStart = dock.indexOf('if (event.altKey && event.shiftKey')
  const reorderEnd = dock.indexOf('\n  }\n  if (event.key ===', reorderStart)
  assert.ok(reorderStart >= 0 && reorderEnd > reorderStart)
  assert.match(dock.slice(reorderStart, reorderEnd), /store\.reorderSessions\(index, nextIndex\)/)
  assert.match(dock.slice(reorderStart, reorderEnd), /tabRefs\.get\(sessionID\)\?\.focus\(\)/)
})
