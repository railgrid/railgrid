import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const threadRail = await readFile(new URL('./ThreadRail.vue', import.meta.url), 'utf8')
const splitHelperSource = app.match(/function splitPercentFromPointer\([\s\S]*?\n\}/)?.[0]
const splitBoundsSource = app.match(/const SPLIT_MIN_PERCENT = 32\nconst SPLIT_MAX_PERCENT = 68/)?.[0]
const conversationMinimumConstantSource = app.match(/const CONVERSATION_BASE_MIN_WIDTH = 240/)?.[0]
const conversationMinimumHelperSource = app.match(/function conversationMinimumWidthForLayout\([\s\S]*?\n\}/)?.[0]
assert.ok(splitHelperSource, 'App.vue should define the split-region pointer geometry helper')
assert.ok(splitBoundsSource, 'App.vue should define split bounds')
assert.ok(conversationMinimumConstantSource, 'App.vue should define the chat minimum width')
assert.ok(conversationMinimumHelperSource, 'App.vue should derive the conversation minimum from the rail layout width')

const { outputText } = ts.transpileModule(`${splitBoundsSource}\n${conversationMinimumConstantSource}\n${conversationMinimumHelperSource}\n${splitHelperSource}\nexport { conversationMinimumWidthForLayout, splitPercentFromPointer }`, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const { conversationMinimumWidthForLayout, splitPercentFromPointer } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

function extractFunction(source, name) {
  const start = source.indexOf(`function ${name}`)
  assert.ok(start >= 0, `App.vue should define ${name}`)
  const open = source.indexOf('{', start)
  assert.ok(open > start, `${name} should have a body`)
  let depth = 0
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    if (source[index] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(start, index + 1)
    }
  }
  assert.fail(`${name} has an unterminated body`)
}

const threadRailLayoutSource = threadRail.match(/const layoutWidth = computed\(\(\) => \([\s\S]*?\n\)\)/)?.[0]
assert.ok(threadRailLayoutSource, 'ThreadRail.vue should expose its in-flow layout width')
const threadRailLayoutHarnessSource = ts.transpileModule(`
export function createThreadRailLayoutHarness() {
  const computed = (fn) => ({ get value() { return fn() } })
  const mobileViewport = { value: false }
  const mobileOpen = { value: false }
  const anchored = { value: true }
  const effectiveWidth = { value: 224 }
  ${threadRailLayoutSource}
  return {
    layoutWidth,
    refs: { mobileViewport, mobileOpen, anchored, effectiveWidth },
  }
}
`, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
}).outputText
const { createThreadRailLayoutHarness } = await import(`data:text/javascript;base64,${Buffer.from(threadRailLayoutHarnessSource).toString('base64')}`)

const resizeHarnessSource = ts.transpileModule(`
export function createResizeHarness(rect = { left: 0, width: 1000 }) {
  // App.vue only uses HTMLElement as a runtime guard for the pointer target;
  // a plain object is sufficient for this DOM-free contract harness.
  const HTMLElement = Object
  const window = {
    innerWidth: 1024,
    listeners: new Map(),
    addEventListener(type, listener) {
      const listeners = this.listeners.get(type) ?? new Set()
      listeners.add(listener)
      this.listeners.set(type, listeners)
    },
    removeEventListener(type, listener) {
      this.listeners.get(type)?.delete(listener)
    },
    dispatch(type, event = {}) {
      for (const listener of [...(this.listeners.get(type) ?? [])]) listener(event)
    },
    listenerCount(type) {
      return this.listeners.get(type)?.size ?? 0
    },
  }
  const splitRegionRef = { value: { getBoundingClientRect: () => rect } }
  const splitRegionWidth = { value: rect.width }
  const splitResizing = { value: false }
  const splitWidth = { value: 38 }
  const conversationMinimumWidth = { value: 240 }
  let splitResizePointerID = null
  let splitResizeTarget = null
  const persisted = []
  const synced = []
  function persistSplitWidth() {
    persisted.push(splitWidth.value)
  }
  function syncSplitRegionGeometry() {
    synced.push(splitRegionWidth.value)
  }
  ${splitBoundsSource}
  ${conversationMinimumConstantSource}
  ${conversationMinimumHelperSource}
  ${extractFunction(app, 'splitMinimumPercentForWidth')}
  ${extractFunction(app, 'clampSplitPercentForWidth')}
  ${splitHelperSource}
  ${extractFunction(app, 'startResize')}
  ${extractFunction(app, 'resizeWorkspace')}
  ${extractFunction(app, 'stopResize')}
  ${extractFunction(app, 'handleResizeKeydown')}
  const target = {
    captured: new Set(),
    captureCalls: [],
    releaseCalls: [],
    setPointerCapture(pointerID) {
      this.captureCalls.push(pointerID)
      this.captured.add(pointerID)
    },
    hasPointerCapture(pointerID) {
      return this.captured.has(pointerID)
    },
    releasePointerCapture(pointerID) {
      this.releaseCalls.push(pointerID)
      this.captured.delete(pointerID)
    },
  }
  return {
    window,
    target,
    refs: { splitResizing, splitWidth, splitRegionWidth, conversationMinimumWidth },
    persisted,
    synced,
    startResize,
    resizeWorkspace,
    stopResize,
    handleResizeKeydown,
  }
}
`, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
}).outputText
const { createResizeHarness } = await import(`data:text/javascript;base64,${Buffer.from(resizeHarnessSource).toString('base64')}`)

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

test('derives conversation minimum from anchored rail width without reserving flyout width', () => {
  const railStates = [
    ['collapsed', 0, 240],
    ['flyout', 0, 240],
    ['anchored', 224, 464],
  ]
  for (const [state, layoutWidth, expectedMinimum] of railStates) {
    assert.equal(
      conversationMinimumWidthForLayout(layoutWidth),
      expectedMinimum,
      `${state} rail should contribute only its actual in-flow width`,
    )
  }
 assert.equal(splitPercentFromPointer(0, { left: 0, width: 1000 }, conversationMinimumWidthForLayout(0)), 32)
  assert.ok(Math.abs(splitPercentFromPointer(0, { left: 0, width: 1000 }, conversationMinimumWidthForLayout(224)) - 46.4) < 0.000001)
 assert.equal(splitPercentFromPointer(0, { left: 0, width: 500 }, conversationMinimumWidthForLayout(224)), 68)
  assert.equal(splitPercentFromPointer(900, { left: 0, width: 1000 }, conversationMinimumWidthForLayout(0)), 68)
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
  assert.match(conversationPane, /class="flex min-h-0 min-w-0 shrink-0 flex-col md:min-w-\[var\(--conversation-min-width\)\]"/)
  assert.match(conversationPane, /md:min-w-\[var\(--conversation-min-width\)\]/)
  assert.doesNotMatch(conversationPane, /432px/)
  assert.match(app, /const conversationMinimumWidth = computed\(\(\) => conversationMinimumWidthForLayout\(threadRailRef\.value\?\.layoutWidth \?\? 0\)\)/)
  assert.match(app, /'--conversation-min-width': `\$\{conversationMinimumWidth\.value\}px`/)
  assert.match(threadRail, /const layoutWidth = computed\(\(\) => \([\s\S]*!mobileViewport\.value[\s\S]*!mobileOpen\.value[\s\S]*anchored\.value[\s\S]*effectiveWidth\.value[\s\S]*: 0\n\)\)/)
  assert.match(threadRail, /layoutWidth,/)

  const chatSectionStart = app.indexOf('<section class="flex min-h-[360px]', railStart)
  const chatSectionEnd = app.indexOf('>', chatSectionStart)
  const chatSection = app.slice(chatSectionStart, chatSectionEnd + 1)
  assert.match(chatSection, /md:min-w-\[240px\]/)
  assert.doesNotMatch(chatSection, /(?:^| )min-w-\[240px\]/)
})

test('keeps the main divider captured and keyboard accessible across terminal pointer paths', () => {
  assert.match(app, /setPointerCapture\(e\.pointerId\)/)
  assert.match(app, /window\.addEventListener\('pointercancel', stopResize\)/)
  assert.match(app, /window\.addEventListener\('blur', stopResize\)/)
  assert.match(app, /window\.removeEventListener\('pointercancel', stopResize\)/)
  assert.match(app, /target\.releasePointerCapture\(activePointerID\)/)
  assert.match(app, /@pointercancel="stopResize"/)
  assert.match(app, /@lostpointercapture="stopResize"/)
  assert.doesNotMatch(app, /@pointermove="resizeWorkspace"/)
  assert.match(app, /role="separator"[\s\S]*aria-orientation="vertical"[\s\S]*tabindex="0"/)
  assert.match(app, /:aria-valuemin="splitMinimumPercent"[\s\S]*:aria-valuemax="SPLIT_MAX_PERCENT"[\s\S]*:aria-valuenow="renderedSplitWidth"/)
  assert.match(app, /function handleResizeKeydown\(event: KeyboardEvent\)[\s\S]*\['ArrowLeft', 'ArrowRight', 'Home', 'End'\]/)
  assert.match(app, /function handleResizeKeydown\(event: KeyboardEvent\)[\s\S]*event\.preventDefault\(\)/)
  const unmountStart = app.indexOf('onBeforeUnmount(() => {\n  appComponentMounted = false')
  const unmountEnd = app.indexOf('\n\nasync function load', unmountStart)
  assert.ok(unmountStart >= 0 && unmountEnd > unmountStart)
  assert.match(app.slice(unmountStart, unmountEnd), /stopResize\(\)/)
})

test('keeps workbench visibility independent from split width and tab state', () => {
  assert.match(app, /const workbenchVisible = ref\(readWorkbenchVisibility\(\)\)/)
  assert.match(app, /function toggleWorkbenchPane\(event\?: MouseEvent\)/)
  assert.match(app, /writeWorkbenchVisibility\(nextVisible\)/)
  assert.match(app, /:aria-expanded="workbenchVisible"[\s\S]*aria-controls="app-studio-workbench-pane"/)
  assert.match(app, /data-app-studio-workbench-toggle/)
  assert.match(app, /v-show="workbenchVisible"[\s\S]*data-app-studio-workbench-pane/)
  assert.match(app, /flexBasis: workbenchVisible\.value \? `\$\{renderedSplitWidth\.value\}%` : '100%'/)
  assert.match(app, /function revealWorkbenchPane\(\)[\s\S]*workbenchVisible\.value = true/)
  assert.match(app, /function openBuiltInWorkbenchTab\(kind: WorkbenchBuiltInTab\) \{\n  revealWorkbenchPane\(\)/)
  assert.match(app, /function openTool\(tool: ProviderTool\) \{\n  revealWorkbenchPane\(\)/)
  const workbenchHeaderStart = app.indexOf('<header class="flex h-14', app.indexOf('<section data-app-studio-workbench-pane'))
  const workbenchHeaderEnd = app.indexOf('</header>', workbenchHeaderStart)
  assert.doesNotMatch(app.slice(workbenchHeaderStart, workbenchHeaderEnd), /<PanelRight/)
  assert.equal((app.match(/data-app-studio-workbench-toggle/g) ?? []).length, 1)
})

test('reserves only an anchored desktop rail in the conversation minimum', () => {
  const harness = createThreadRailLayoutHarness()
  assert.equal(harness.layoutWidth.value, 224)
  assert.equal(conversationMinimumWidthForLayout(harness.layoutWidth.value), 464)

  harness.refs.anchored.value = false
  assert.equal(harness.layoutWidth.value, 0, 'a collapsed/flyout rail must not reserve width')
  assert.equal(conversationMinimumWidthForLayout(harness.layoutWidth.value), 240)

  harness.refs.anchored.value = true
  harness.refs.mobileOpen.value = true
  assert.equal(harness.layoutWidth.value, 0, 'an open mobile rail must not reserve desktop width')
  harness.refs.mobileOpen.value = false
  harness.refs.mobileViewport.value = true
  assert.equal(harness.layoutWidth.value, 0, 'a mobile viewport must not reserve rail width')

  harness.refs.mobileViewport.value = false
  harness.refs.effectiveWidth.value = 312
  assert.equal(harness.layoutWidth.value, 312, 'the desktop rail should contribute its effective in-flow width')
  assert.equal(conversationMinimumWidthForLayout(harness.layoutWidth.value), 552)
})

test('uses one captured pointer stream and cleans it up on every terminal path', () => {
  const pointerDown = {
    pointerId: 7,
    currentTarget: null,
    preventDefault() {},
    stopPropagation() {},
  }

  for (const terminal of ['pointerup', 'pointercancel', 'lostpointercapture', 'blur']) {
    const harness = createResizeHarness()
    pointerDown.currentTarget = harness.target
    harness.startResize(pointerDown)

    assert.equal(harness.refs.splitResizing.value, true, `${terminal}: pointerdown should start resizing`)
    assert.deepEqual(harness.target.captureCalls, [7], `${terminal}: pointerdown should capture its pointer`)
    assert.equal(harness.window.listenerCount('pointermove'), 1, `${terminal}: one window move listener is registered`)
    assert.equal(harness.window.listenerCount('pointerup'), 1, `${terminal}: one window pointerup listener is registered`)
    assert.equal(harness.window.listenerCount('pointercancel'), 1, `${terminal}: one window cancel listener is registered`)
    assert.equal(harness.window.listenerCount('blur'), 1, `${terminal}: one window blur listener is registered`)

    // This dispatch models the pointer continuing over an iframe: the divider
    // receives no local move event, but the captured/window stream still does.
    harness.window.dispatch('pointermove', { pointerId: 7, clientX: 600 })
    assert.equal(harness.refs.splitWidth.value, 60, `${terminal}: captured move should update the split`)

    if (terminal === 'lostpointercapture') harness.stopResize({ pointerId: 7 })
    else harness.window.dispatch(terminal, terminal === 'blur' ? {} : { pointerId: 7 })

    assert.equal(harness.refs.splitResizing.value, false, `${terminal}: terminal event should stop resizing`)
    assert.equal(harness.window.listenerCount('pointermove'), 0, `${terminal}: move listener should be removed`)
    assert.equal(harness.window.listenerCount('pointerup'), 0, `${terminal}: pointerup listener should be removed`)
    assert.equal(harness.window.listenerCount('pointercancel'), 0, `${terminal}: cancel listener should be removed`)
    assert.equal(harness.window.listenerCount('blur'), 0, `${terminal}: blur listener should be removed`)
    assert.deepEqual(harness.target.releaseCalls, [7], `${terminal}: pointer capture should be released`)
    assert.equal(harness.persisted.length, 1, `${terminal}: split should persist once`)
    assert.equal(harness.synced.length, 1, `${terminal}: geometry should synchronize once`)

    // A duplicate bubbled terminal event must be harmless after cleanup.
    harness.stopResize({ pointerId: 7 })
    assert.equal(harness.persisted.length, 1, `${terminal}: duplicate terminal events must not persist twice`)
    assert.deepEqual(harness.target.releaseCalls, [7], `${terminal}: duplicate terminal events must not release twice`)
  }
})

test('ignores another pointer and keyboard-resizes within the live minimum', () => {
  const harness = createResizeHarness()
  const pointerDown = {
    pointerId: 7,
    currentTarget: harness.target,
    preventDefault() {},
    stopPropagation() {},
  }
  harness.startResize(pointerDown)
  harness.stopResize({ pointerId: 8 })
  assert.equal(harness.refs.splitResizing.value, true, 'a different pointer must not terminate the active resize')
  assert.equal(harness.window.listenerCount('pointermove'), 1)

  const key = (name, shiftKey = false) => {
    let prevented = false
    harness.handleResizeKeydown({
      key: name,
      shiftKey,
      preventDefault() { prevented = true },
    })
    return prevented
  }
  harness.stopResize({ pointerId: 7 })
  harness.refs.splitWidth.value = 40
  assert.equal(key('ArrowLeft'), true)
  assert.equal(harness.refs.splitWidth.value, 38)
  assert.equal(key('ArrowRight', true), true)
  assert.equal(harness.refs.splitWidth.value, 46)
  assert.equal(key('Home'), true)
  assert.equal(harness.refs.splitWidth.value, 32)
  assert.equal(key('End'), true)
  assert.equal(harness.refs.splitWidth.value, 68)

  harness.refs.splitWidth.value = 40
  // The harness exposes the same live ref used by resizeWorkspace and the
  // keyboard handler, so an anchored rail can force the lower bound up.
  harness.refs.conversationMinimumWidth.value = 464
  assert.equal(key('Home'), true)
  assert.ok(Math.abs(harness.refs.splitWidth.value - 46.4) < 0.000001)

  harness.refs.splitWidth.value = 46.4
  assert.equal(key('PageDown'), false, 'unrelated keys must not be consumed')
  assert.equal(harness.refs.splitWidth.value, 46.4)
})

test('keeps the desktop split and mobile stack structurally distinct', () => {
  const splitRegionStart = app.indexOf('<div ref="splitRegionRef"')
  const splitRegionEnd = app.indexOf('>', splitRegionStart)
  const splitRegion = app.slice(splitRegionStart, splitRegionEnd + 1)
  assert.match(splitRegion, /flex-col/)
  assert.match(splitRegion, /md:flex-row/)

  const dividerMarker = app.indexOf('v-show="workbenchVisible"', splitRegionStart)
  const dividerStart = app.lastIndexOf('<div', dividerMarker)
  const dividerEnd = app.indexOf('>', dividerStart)
  const divider = app.slice(dividerStart, dividerEnd + 1)
  assert.match(divider, /v-show="workbenchVisible"/)
  assert.match(divider, /class="hidden[^\"]*md:flex/)

  const workbenchStart = app.indexOf('<section data-app-studio-workbench-pane')
  const workbenchEnd = app.indexOf('>', workbenchStart)
  const workbenchPane = app.slice(workbenchStart, workbenchEnd + 1)
  assert.match(workbenchPane, /v-show="workbenchVisible"/)
  assert.match(workbenchPane, /min-h-\[360px\]/)
  assert.match(workbenchPane, /md:min-h-0/)
  assert.match(workbenchPane, /:aria-hidden="!workbenchVisible"/)

  assert.match(threadRail, /mobileOpen \? 'absolute inset-y-0 left-0 block w-64' : 'relative hidden md:block'/)
  assert.match(threadRail, /!mobileOpen && \(anchored \? 'md:w-\[var\(--thread-rail-width\)\]' : 'md:w-0'\)/)
})

test('reveals the hidden workbench through every explicit tab and tool action', () => {
  const toggleStart = app.indexOf('function toggleWorkbenchPane')
  const toggleEnd = app.indexOf('\nfunction openBuiltInWorkbenchTab', toggleStart)
  const toggleSource = app.slice(toggleStart, toggleEnd)
  assert.ok(toggleStart >= 0 && toggleEnd > toggleStart)
  assert.match(toggleSource, /workbenchPaneRef\.value\?\.contains\(activeElement\)/)
  assert.match(toggleSource, /trigger\?\.focus\(\)/)
  assert.match(toggleSource, /if \(!nextVisible\) stopResize\(\)/)
  assert.ok(toggleSource.indexOf('trigger?.focus()') < toggleSource.indexOf('workbenchVisible.value = nextVisible'))
  assert.doesNotMatch(toggleSource, /workbench\.value\s*=|splitWidth\.value\s*=/)

  for (const signature of [
    'function openBuiltInWorkbenchTab',
    'function openWorkbenchLauncherItem',
    'function selectExistingWorkbenchLauncherTab',
    'function activateWorkbenchTabByID',
    'function openTool',
  ]) {
    const start = app.indexOf(signature)
    const next = app.indexOf('\nfunction ', start + signature.length)
    const source = app.slice(start, next < 0 ? undefined : next)
    assert.ok(start >= 0, `${signature} should exist`)
    assert.match(source, /revealWorkbenchPane\(\)/, `${signature} should reveal a hidden workbench`)
  }
})

test('coordinates the Workbench dock with the conversation layout and reduced motion', () => {
  assert.match(app, /<Transition name="workbench-pane">[\s\S]*data-app-studio-workbench-pane/)
  assert.match(app, /<Transition name="workbench-divider">[\s\S]*ref="splitResizeDividerRef"/)
  assert.match(app, /'workbench-conversation-pane'/)
  assert.match(app, /workbenchVisible \? 'workbench-conversation-entering' : 'workbench-conversation-leaving'/)

  const styleStart = app.indexOf('<style scoped>')
  assert.ok(styleStart >= 0, 'App.vue should define scoped Workbench motion styles')
  const style = app.slice(styleStart)
  assert.match(style, /transition: transform 280ms cubic-bezier\(0\.16, 1, 0\.3, 1\), opacity 280ms ease-out/)
  assert.match(style, /transition: transform 190ms cubic-bezier\(0\.16, 1, 0\.3, 1\), opacity 190ms ease-out/)
  assert.match(style, /transition: flex-basis 280ms cubic-bezier\(0\.16, 1, 0\.3, 1\)/)
  assert.match(style, /workbench-conversation-pane\.workbench-conversation-leaving[\s\S]*transition-duration: 190ms/)
  assert.match(style, /@media \(prefers-reduced-motion: reduce\)/)
  const reducedMotionStart = style.indexOf('@media (prefers-reduced-motion: reduce)')
  const reducedMotion = style.slice(reducedMotionStart)
  assert.match(reducedMotion, /workbench-conversation-pane[\s\S]*transition: none/)
  assert.match(reducedMotion, /workbench-pane-enter-active,[\s\S]*workbench-divider-leave-active \{\n    transition: none;/)
  assert.match(reducedMotion, /workbench-pane-enter-from,[\s\S]*transform: none/)
  assert.doesNotMatch(style, /bounce|blur|accent-glow/)
})
