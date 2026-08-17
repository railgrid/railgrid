import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true } })
})
test.after(async () => vite?.close())

test('renders preview secondary actions behind an accessible overflow button', async () => {
  const { default: PreviewActionsMenu } = await vite.ssrLoadModule('/src/PreviewActionsMenu.vue')
  const html = await renderToString(createSSRApp(PreviewActionsMenu, {
    templates: [{ name: 'application', displayName: 'Web application' }],
    currentTemplate: 'application',
  }))
  assert.match(html, /aria-label="More preview actions"/)
  assert.match(html, /aria-haspopup="dialog"/)
  assert.match(html, /aria-expanded="false"/)
  assert.doesNotMatch(html, /Load from git/)
})

test('keeps template switching and git hydration inside the overflow menu', async () => {
  const source = await readFile(new URL('./PreviewActionsMenu.vue', import.meta.url), 'utf8')
  assert.match(source, /role="dialog"/)
  assert.match(source, /aria-modal="false"/)
  assert.match(source, />Switch template</)
  assert.match(source, /Development templates/)
  assert.match(source, /Load from git/)
  assert.match(source, /emit\('selectTemplate', template\)/)
  assert.match(source, /emit\('loadFromGit'\)/)
  assert.match(source, /event\.key !== 'Escape'/)
})

test('leaves only primary preview actions visible in the toolbar', async () => {
  const source = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const toolbar = source.slice(source.indexOf('<PreviewActionsMenu'), source.indexOf('</div>', source.indexOf('{{ developmentPreviewOpenButtonLabel }}')))
  assert.match(toolbar, /<PreviewActionsMenu/)
  assert.match(toolbar, /developmentPreviewAnnotationMode \? 'Annotating' : 'Annotate'/)
  assert.match(toolbar, /:aria-pressed="developmentPreviewAnnotationMode"/)
  assert.match(toolbar, /title="Sync"/)
  assert.match(toolbar, /developmentPreviewOpenButtonLabel/)
  assert.doesNotMatch(toolbar, /aria-label="Development preview access"/)
  assert.equal((toolbar.match(/<select/g) ?? []).length, 0)
  assert.doesNotMatch(toolbar, />Switch template</)
})

test('keeps annotation visible as a first-class preview action with an anchored comment editor', async () => {
  const menu = await readFile(new URL('./PreviewActionsMenu.vue', import.meta.url), 'utf8')
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(menu, /Annotate preview/)
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
  assert.match(app, /type PreviewConsoleAnnotationPinHover/)
  assert.match(app, /onAnnotationPinHover: handleDevelopmentPreviewAnnotationPinHover/)
  assert.match(app, /function handleDevelopmentPreviewAnnotationPinHover\(hover: PreviewConsoleAnnotationPinHover\)/)
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
  const menu = await readFile(new URL('./PreviewActionsMenu.vue', import.meta.url), 'utf8')
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(menu, /disabled\?: boolean/)
  assert.match(menu, /props\.disabled \|\| props\.templateBusy/)
  assert.match(menu, /props\.disabled \|\| props\.hydrateBusy/)
  assert.match(app, /:disabled="messageStreaming"/)
  assert.match(app, /messageStreaming \|\| developmentSyncBusy/)
})
