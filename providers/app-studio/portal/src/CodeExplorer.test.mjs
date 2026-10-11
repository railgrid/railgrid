import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { createServer } from 'vite'

const source = await readFile(new URL('./CodeExplorer.vue', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
const { codeExplorerTreeState } = await vite.ssrLoadModule('/src/CodeExplorer.vue')
const { selectedProjectFileDeleteVersion } = await vite.ssrLoadModule('/src/projectFileControls.ts')
test.after(async () => vite.close())

const setupScript = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)?.[1]
const setupAST = ts.createSourceFile('CodeExplorer.ts', setupScript ?? '', ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)

function componentFunction(name) {
  for (const statement of setupAST.statements) {
    if (ts.isFunctionDeclaration(statement) && statement.name?.text === name) return statement
  }
  throw new Error(`CodeExplorer ${name} function was not found`)
}

function transpileFunction(name) {
  return ts.transpile(componentFunction(name).getText(setupAST), { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.None })
}

const failureMessageFunction = new Function(
  'isProjectFileRequestError',
  'errorMessage',
  `${transpileFunction('projectFileFailureMessage')}\nreturn projectFileFailureMessage`,
)((error) => typeof error?.reason === 'string', (error, fallback) => error instanceof Error && error.message ? error.message : fallback)

function uploadHarness({ uploadProjectFiles, readProjectFile, confirm = async () => true }) {
  const projectContext = {}
  const notifications = []
  const refreshed = []
  const writeBusy = { value: false }
  const writeStatus = { value: '' }
  const dependencies = {
    props: { projectName: 'shop', ctx: projectContext },
    writeBusy,
    writeStatus,
    acceptUploadCandidates: (files) => files,
    isCurrentProject: (projectName, ctx) => projectName === 'shop' && ctx === projectContext,
    api: { uploadProjectFiles, readProjectFile },
    joinProjectFilePath: (dir, name) => dir ? `${dir}/${name}` : name,
    isProjectFileRequestError: (error) => typeof error?.reason === 'string',
    errorMessage: (error, fallback) => error instanceof Error && error.message ? error.message : fallback,
    projectFileFailureMessage: failureMessageFunction,
    confirmDialog: confirm,
    toast: (...args) => notifications.push(args),
    dirLabel: (dir) => dir || 'project root',
    refreshAndSelect: async (...args) => refreshed.push(args),
  }
  const uploadFiles = new Function(...Object.keys(dependencies), `${transpileFunction('uploadFiles')}\nreturn uploadFiles`)(...Object.values(dependencies))
  return { uploadFiles, notifications, refreshed, writeBusy, writeStatus, projectContext }
}

test('distinguishes initial tree hydration from refresh and does not show a false empty state', () => {
  assert.match(source, /class="shimmer h-4 rounded bg-surface-overlay"/)
  assert.match(source, /Loading workspace files…/)
  assert.match(source, /treeState === 'refreshing'/)
  assert.match(source, /treeState === 'refresh-error'/)
  assert.match(source, /Showing the last loaded tree\./)
  assert.match(source, /treeState === 'empty'/)
  assert.match(source, /role="alert"/)
})

test('guards tree and file responses with request serials and current project checks', () => {
  assert.match(source, /let treeRequestSerial = 0/)
  assert.match(source, /let fileRequestSerial = 0/)
  assert.match(source, /serial !== treeRequestSerial \|\| !isCurrentProject\(projectName, requestContext\)/)
  assert.match(source, /serial !== fileRequestSerial \|\| !isCurrentProject\(projectName, requestContext\)/)
  assert.match(source, /selectedPath\.value !== path/)
  assert.match(source, /treeRequestSerial\+\+/)
  assert.match(source, /fileRequestSerial\+\+/)
})

test('keeps a cached tree visible and reports refresh failure separately from initial failure', () => {
  assert.equal(codeExplorerTreeState(true, false, null), 'initial-loading')
  assert.equal(codeExplorerTreeState(false, false, 'Could not load files.'), 'initial-error')
  assert.equal(codeExplorerTreeState(true, true, null), 'refreshing')
  assert.equal(codeExplorerTreeState(false, true, 'Could not refresh files.'), 'refresh-error')
  assert.equal(codeExplorerTreeState(false, true, null), 'ready')
  assert.match(source, /class="app-studio-touch-target[^\"]*focus-visible:ring-2[^\"]*" @click="refreshWorkspaceSnapshot">Retry refresh<\/button>/)
})

test('adapts the explorer to one pane on mobile and exposes a complete keyboard tree', () => {
  assert.match(source, /flex-col md:flex-row/)
  assert.match(source, /mobileTreeOpen \? 'flex' : 'hidden md:flex'/)
  assert.match(source, /mobileTreeOpen \? 'hidden md:flex' : 'flex'/)
  assert.match(source, /aria-label="Back to workspace files"/)
  assert.match(source, /ref="mobileBackRef"[\s\S]*aria-label="Back to workspace files"/)
  assert.match(source, /role="tree"/)
  assert.match(source, /role="treeitem"/)
  assert.match(source, /:aria-level="row\.depth \+ 1"/)
  assert.match(source, /:aria-posinset="row\.posInSet"/)
  assert.match(source, /:aria-setsize="row\.setSize"/)
  assert.match(source, /:aria-expanded="row\.node\.dir/)
  assert.match(source, /:aria-selected="!row\.node\.dir/)
  assert.match(source, /event\.key === 'ArrowDown'/)
  assert.match(source, /event\.key === 'ArrowRight'/)
  assert.match(source, /event\.key === 'Home'/)
})

test('refreshes the tree and selected file after a workspace revision changes', () => {
  assert.match(source, /refreshRevision: number/)
  assert.match(source, /async function refreshWorkspaceSnapshot\(\)[\s\S]*const path = selectedPath\.value[\s\S]*await loadTree\(\)[\s\S]*await openFile\(path, projectName, requestContext\)/)
  assert.match(source, /\(\) => props\.refreshRevision[\s\S]*void refreshWorkspaceSnapshot\(\)/)
  assert.match(source, /@click="refreshWorkspaceSnapshot"/)
})

test('only enables deletion for the loaded version of the selected file', () => {
  const snapshot = { selectedPath: 'src/main.ts', content: null, loading: true, error: null }
  assert.equal(selectedProjectFileDeleteVersion(snapshot), undefined)
  snapshot.loading = false
  snapshot.content = { path: 'src/main.ts' }
  assert.equal(selectedProjectFileDeleteVersion(snapshot), undefined)
  snapshot.content.version = 'sha256:loaded'
  assert.equal(selectedProjectFileDeleteVersion(snapshot), 'sha256:loaded')
  snapshot.content = { path: 'README.md', version: 'sha256:other' }
  assert.equal(selectedProjectFileDeleteVersion(snapshot), undefined)
  snapshot.content = { path: 'src/main.ts', version: 'sha256:loaded' }
  snapshot.error = 'The file could not be refreshed.'
  assert.equal(selectedProjectFileDeleteVersion(snapshot), undefined)

  assert.match(source, /const version = selectedDeleteVersion\.value[\s\S]*selectedPath\.value !== path \|\| selectedDeleteVersion\.value !== version[\s\S]*ifMatch: version/)
  assert.match(source, /selectedPath\.value !== path \|\| selectedDeleteVersion\.value !== version[\s\S]*toast\('error', `\$\{path\} changed while you were confirming the delete/)
  assert.match(source, /:disabled="writesDisabled \|\| !selectedDeleteVersion"/)
})

test('replaces with the read version and surfaces a stale conflict without retrying', async () => {
  const requests = []
  const harness = uploadHarness({
    async uploadProjectFiles(_ctx, _project, files, options) {
      requests.push({ name: files[0].name, options })
      if (!options.overwrite) throw { reason: 'exists', message: 'A file already exists at that path.' }
      throw Object.assign(new Error('The file changed since it was loaded. Refresh and try again.'), {
        reason: 'changed',
        changedFiles: ['docs/notes.md'],
      })
    },
    async readProjectFile(_ctx, _project, path) {
      assert.equal(path, 'docs/notes.md')
      return { path, version: 'sha256:before-confirmation' }
    },
  })

  await harness.uploadFiles([{ name: 'notes.md' }], 'docs')

  assert.equal(requests.length, 2, 'a stale replacement is reported without another write attempt')
  assert.deepEqual(requests[0], { name: 'notes.md', options: { dir: 'docs' } })
  assert.deepEqual(requests[1], {
    name: 'notes.md',
    options: { dir: 'docs', overwrite: true, expectedVersions: { 'docs/notes.md': 'sha256:before-confirmation' } },
  })
  assert.deepEqual(harness.notifications, [[
    'error',
    'The file changed since it was loaded. Refresh and try again. Changed: docs/notes.md.',
  ]])
  assert.deepEqual(harness.refreshed, [])
  assert.equal(harness.writeBusy.value, false)
  assert.equal(harness.writeStatus.value, '')
})

test('advances the explorer revision only for an accepted terminal workspace mutation', () => {
  assert.match(appSource, /const codeExplorerRefreshRevision = ref\(0\)/)
  assert.match(appSource, /assistantRunTerminal\(normalized\.run\.status\) && acceptedTerminal[\s\S]*normalized\.message\.metadata\?\.previewRefreshNeeded === true[\s\S]*codeExplorerRefreshRevision\.value \+= 1/)
  assert.match(appSource, /<CodeExplorer[\s\S]*:refresh-revision="codeExplorerRefreshRevision"/)
})
