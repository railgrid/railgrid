import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const helperSource = app.match(/function splitPercentFromPointer\([\s\S]*?\n\}/)?.[0]
assert.ok(helperSource, 'App.vue should define the split-region pointer geometry helper')

const { outputText } = ts.transpileModule(`${helperSource}\nexport { splitPercentFromPointer }`, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const { splitPercentFromPointer } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

test('keeps divider geometry invariant across anchored, collapsed, and flyout thread rails', () => {
  const splitRegion = { left: 84, width: 1000 }
  const dividerX = splitRegion.left + splitRegion.width * 0.58

  for (const railState of ['anchored', 'collapsed', 'flyout']) {
    assert.ok(
      Math.abs(splitPercentFromPointer(dividerX, splitRegion) - 58) < 0.000001,
      `${railState} rail must not change the split-region origin or width`,
    )
  }
  assert.equal(splitPercentFromPointer(splitRegion.left + 100, splitRegion), 32)
  assert.equal(splitPercentFromPointer(splitRegion.left + 900, splitRegion), 68)
})

test('ignores unusable split-region geometry without producing a bad width', () => {
  assert.equal(splitPercentFromPointer(400, { left: 0, width: 0 }), null)
  assert.equal(splitPercentFromPointer(Number.NaN, { left: 0, width: 1000 }), null)
})

test('keeps the title bar and thread rail inside the resizable left group', () => {
  const splitStart = app.indexOf('<div ref="splitRegionRef"')
  const leftGroupStart = app.indexOf('<section data-app-studio-conversation-pane', splitStart)
  const titleBarStart = app.indexOf('<header data-app-studio-titlebar', leftGroupStart)
  const railStart = app.indexOf('<ThreadRail', titleBarStart)
  const dividerStart = app.indexOf('@pointerdown="startResize"', railStart)
  const workbenchStart = app.indexOf('<section data-app-studio-workbench-pane', dividerStart)
  const workbenchHeaderStart = app.indexOf('<header class="flex h-14', workbenchStart)

  assert.ok(splitStart >= 0)
  assert.ok(leftGroupStart > splitStart)
  assert.ok(titleBarStart > leftGroupStart)
  assert.ok(railStart > titleBarStart)
  assert.ok(dividerStart > railStart)
  assert.ok(workbenchStart > dividerStart)
  assert.ok(workbenchHeaderStart > workbenchStart)
  assert.match(app.slice(leftGroupStart, dividerStart), /:style="conversationPaneStyle"/)

  const conversationPaneStart = app.indexOf('<section data-app-studio-conversation-pane', leftGroupStart)
  const conversationPaneEnd = app.indexOf('>', conversationPaneStart)
  const conversationPane = app.slice(conversationPaneStart, conversationPaneEnd + 1)
  assert.match(conversationPane, /class="flex min-h-0 min-w-0 shrink-0 flex-col md:min-w-\[432px\]"/)
  assert.match(conversationPane, /md:min-w-\[432px\]/)
  assert.doesNotMatch(conversationPane, /(?:^| )min-w-\[432px\]/)

  const chatSectionStart = app.indexOf('<section class="flex min-h-[360px]', railStart)
  const chatSectionEnd = app.indexOf('>', chatSectionStart)
  const chatSection = app.slice(chatSectionStart, chatSectionEnd + 1)
  assert.match(chatSection, /md:min-w-\[240px\]/)
  assert.doesNotMatch(chatSection, /(?:^| )min-w-\[240px\]/)
})
