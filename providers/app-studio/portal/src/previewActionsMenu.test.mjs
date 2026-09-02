import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'

async function importTypeScriptModule(relativePath) {
  const source = await readFile(new URL(relativePath, import.meta.url), 'utf8')
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  })
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
}

test('moves environment mutations out of the preview toolbar', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const history = await readFile(new URL('./ProjectHistory.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(app, /<PreviewActionsMenu/)
  assert.match(app, /aria-label="Development settings"/)
  assert.match(app, /id="development-template-heading"[\s\S]*>Template</)
  assert.match(app, /id="development-preview-access-heading"[\s\S]*>Preview access</)
  assert.match(app, /aria-label="Development template"/)
  assert.match(app, /@change="changeDevelopmentTemplate"/)
  assert.doesNotMatch(history, /Load from Git|loadFromGit|hydrate/i)
})

test('leaves only primary preview actions visible in the toolbar', async () => {
  const source = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const toolbar = await readFile(new URL('./DevelopmentPreviewToolbar.vue', import.meta.url), 'utf8')
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const toolbarStart = source.indexOf('v-else-if="activeWorkbenchTab?.kind === \'preview\'"')
  const preview = source.slice(toolbarStart, source.indexOf('<div v-if="developmentSyncError', toolbarStart))
  assert.match(preview, /<DevelopmentPreviewToolbar/)
  assert.match(preview, /:annotation-mode="developmentPreviewAnnotationMode"/)
  assert.match(preview, /@annotate="toggleDevelopmentPreviewAnnotation"/)
  assert.match(preview, /@sync="syncDevelopmentPreview"/)
  assert.match(preview, /@open-browser="openDevelopmentPreviewInBrowser"/)
  assert.match(toolbar, /annotationMode \? 'Stop annotating' : 'Annotate preview'/)
  assert.match(toolbar, /:aria-pressed="annotationMode"/)
  assert.match(toolbar, /:data-k-tip="syncBusy \? 'Syncing preview…' : 'Sync preview'"/)
  assert.match(toolbar, /:data-k-tip="openLabel"/)
  assert.match(toolbar, /data-k-tip="More preview actions"/)
  assert.doesNotMatch(toolbar, /after:(?:content|top|right|bottom|left|translate)/)
  assert.match(styles, /\.preview-toolbar \[data-k-tip\]::after\s*\{[\s\S]*content: attr\(data-k-tip\);[\s\S]*inset: calc\(100% \+ 6px\) 0 auto auto;[\s\S]*transform: none;/)
  assert.doesNotMatch(toolbar, /title="(?:Sync|Open a separate browser tab|More preview actions)"/)
  const expandedOpenButton = toolbar.slice(
    toolbar.indexOf(':aria-label="openLabel"'),
    toolbar.indexOf('</button>', toolbar.indexOf(':aria-label="openLabel"')),
  )
  assert.match(expandedOpenButton, /<ExternalLink/)
  assert.doesNotMatch(expandedOpenButton, /\{\{\s*openLabel\s*\}\}/)
  assert.match(toolbar, /More preview actions/)
  assert.match(toolbar, /role="menu"/)
  assert.match(toolbar, /role="menuitem"/)
  assert.match(toolbar, /aria-haspopup="menu"/)
  assert.match(toolbar, /v-if="layout !== 'collapsed'"[\s\S]*preview-toolbar__primary-action/)
  assert.equal((toolbar.match(/v-if="layout === 'expanded'"/g) ?? []).length, 2)
  assert.match(toolbar, /v-if="layout !== 'expanded'" class="preview-toolbar__overflow/)
  assert.match(toolbar, /v-if="layout === 'collapsed'"[\s\S]*role="menuitemcheckbox"/)
  assert.doesNotMatch(toolbar, /Load from Git/i)
  assert.doesNotMatch(toolbar, /aria-label="Development preview access"/)
  assert.equal((toolbar.match(/<select/g) ?? []).length, 0)
  assert.doesNotMatch(toolbar, />Switch template</)
})

test('keeps preview tooltips above the iframe and annotation overlays', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const toolbar = await readFile(new URL('./DevelopmentPreviewToolbar.vue', import.meta.url), 'utf8')
  const toolbarRoot = toolbar.slice(toolbar.indexOf('<header'), toolbar.indexOf('>', toolbar.indexOf('<header')))
  const previewStart = app.indexOf('v-else-if="activeWorkbenchTab?.kind === \'preview\'"')
  const preview = app.slice(previewStart, app.indexOf("v-else-if=\"activeWorkbenchTab?.kind === 'review'\"", previewStart))

  assert.match(toolbarRoot, /class="[^"]*\brelative\b[^"]*\bz-40\b[^"]*"/)
  assert.match(preview, /class="pointer-events-none absolute inset-0 z-30"/)
  assert.match(preview, /<iframe/)
})

test('selects one mutually exclusive preview toolbar layout from its measured width', async () => {
  const { previewToolbarLayout } = await importTypeScriptModule('./previewToolbarLayout.ts')
  assert.equal(previewToolbarLayout(900), 'expanded')
  assert.equal(previewToolbarLayout(521), 'expanded')
  assert.equal(previewToolbarLayout(520), 'compact')
  assert.equal(previewToolbarLayout(381), 'compact')
  assert.equal(previewToolbarLayout(380), 'collapsed')
  assert.equal(previewToolbarLayout(0), 'collapsed')
  assert.equal(previewToolbarLayout(Number.NaN), 'collapsed')
})

test('keeps the responsive preview overflow keyboard and pointer accessible', async () => {
  const toolbar = await readFile(new URL('./DevelopmentPreviewToolbar.vue', import.meta.url), 'utf8')
  assert.match(toolbar, /@keydown\.down\.stop\.prevent="openOverflow\(true\)"/)
  assert.match(toolbar, /\['ArrowDown', 'ArrowUp', 'Home', 'End'\]/)
  assert.match(toolbar, /event\.key === 'Escape'[\s\S]*closeOverflow\(true\)/)
  assert.match(toolbar, /document\.addEventListener\('pointerdown', handleDocumentPointerDown\)/)
  assert.match(toolbar, /new ResizeObserver\(\(entries\) => \{[\s\S]*syncToolbarWidth\(entries\[0\]\?\.contentRect\.width\)[\s\S]*closeOverflow\(\)/)
  assert.match(toolbar, /document\.removeEventListener\('pointerdown', handleDocumentPointerDown\)/)
})

test('keeps annotation visible as a first-class preview action with an anchored comment editor', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(app, /<PreviewActionsMenu/)
  assert.match(app, /<DevelopmentPreviewToolbar/)
  assert.match(app, /const developmentPreviewAnnotationEditorStyle = computed/)
  assert.match(app, /const developmentPreviewAnnotationPinSignature = computed\(\(\) => assistantComposerParts\.value/)
  assert.match(app, /const \[validatedPart\] = projectAssistantComposerParts\(\[\{ type: 'annotation', annotation \}\]\)[\s\S]*assistantComposerParts\.value = draft\.annotationID[\s\S]*?: \[\.\.\.assistantComposerParts\.value, validatedPart\][\s\S]*syncDevelopmentPreviewAnnotationPins\(\)/)
  assert.match(app, /syncDevelopmentPreviewAnnotationPins,[\s\S]*\{ flush: 'post' \}/)
  assert.match(app, /const pins: ProjectAssistantAnnotationPin\[\]/)
  assert.doesNotMatch(app, /boundingRect: annotation\.target\.rect![\s\S]*comment: annotation\.comment/)
  assert.doesNotMatch(app, /<DevelopmentPreviewAnnotationPins/)
  assert.doesNotMatch(app, /data-faros-studio-annotation-pin/)
  assert.match(app, /:style="developmentPreviewAnnotationEditorStyle"/)
  assert.match(app, /class="absolute z-20 flex flex-col items-stretch gap-3/)
  assert.equal((app.match(/id="development-preview-annotation-comment"/g) ?? []).length, 1)
  assert.match(app, /<textarea[\s\S]*rows="3"[\s\S]*placeholder="What should change\?"/)
  assert.match(app, /placeholder="What should change\?"/)
  assert.match(app, /title="Cancel annotation"/)
  assert.doesNotMatch(app, /aria-label="Preview annotations"/)
  assert.match(app, /function handleDevelopmentPreviewAnnotationPinSelect/)
  assert.match(app, /aria-label="Delete annotation"/)
  assert.match(app, />Save<\/button>/)
})

test('renders hover comments in a parent-owned, pointer-transparent preview overlay', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /type PreviewBridgeAnnotationPinHover/)
  assert.match(app, /onAnnotationPinHover: handleDevelopmentPreviewAnnotationPinHover/)
  assert.match(app, /function handleDevelopmentPreviewAnnotationPinHover\(hover: PreviewBridgeAnnotationPinHover\)/)
  assert.match(app, /hover\.pagePath !== pagePath/)
  assert.match(app, /candidate\.pagePath === pagePath/)
  const hoverHandler = app.slice(app.indexOf('function handleDevelopmentPreviewAnnotationPinHover'), app.indexOf('function toggleDevelopmentPreviewAnnotation'))
  assert.doesNotMatch(hoverHandler, /candidate\.documentID/)
  assert.match(hoverHandler, /candidate\.id === hover\.id && !candidate\.stale && candidate\.pagePath === pagePath/)
  assert.match(app, /class="pointer-events-none absolute inset-0 z-30"[\s\S]*aria-live="polite"[\s\S]*role="tooltip"/)
  assert.match(app, /developmentPreviewAnnotationHoverAnnotation\.comment/)
  assert.match(app, /clearDevelopmentPreviewAnnotationHover\(\)/)
  const pinSync = app.slice(app.indexOf('function syncDevelopmentPreviewAnnotationPins'), app.indexOf('function commitDevelopmentPreviewAnnotation'))
  assert.doesNotMatch(pinSync, /comment:\s*annotation\.comment/)
})

test('disables external workspace and target changes while an assistant run is active', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /developmentTemplatesLoading \|\| developmentTemplateBusy \|\| messageStreaming/)
  assert.doesNotMatch(app, /hydrateDevelopmentWorkspace|developmentHydrateBusy/)
  assert.match(app, /messageStreaming \|\| developmentSyncBusy/)
})
