import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { createServer } from 'vite'

const source = await readFile(new URL('./CodeExplorer.vue', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
const { codeExplorerTreeState } = await vite.ssrLoadModule('/src/CodeExplorer.vue')
const {
  selectedProjectFileDeleteVersion,
} = await vite.ssrLoadModule('/src/projectFileControls.ts')
test.after(async () => vite.close())

const setupScript = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)?.[1]
const setupAST = ts.createSourceFile('CodeExplorer.ts', setupScript ?? '', ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
let uploadFunction
for (const statement of setupAST.statements) {
  if (ts.isFunctionDeclaration(statement) && statement.name?.text === 'uploadFiles') uploadFunction = statement
}
if (!uploadFunction) throw new Error('CodeExplorer uploadFiles function was not found')
const uploadFunctionJS = ts.transpile(uploadFunction.getText(setupAST), {
  target: ts.ScriptTarget.ES2020,
  module: ts.ModuleKind.None,
})

function uploadHarness({ uploadProjectFiles, readProjectFile, current = () => true }) {
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
    isCurrentProject: (projectName, ctx) => projectName === 'shop' && ctx === projectContext && current(),
    api: { uploadProjectFiles, readProjectFile },
    joinProjectFilePath: (dir, name) => dir ? `${dir}/${name}` : name,
    isProjectFileRequestError: (error) => typeof error?.reason === 'string',
    errorMessage: (error, fallback) => error instanceof Error && error.message ? error.message : fallback,
    confirmDialog: async () => { throw new Error('baseline read failure should not ask for confirmation') },
    toast: (...args) => notifications.push(args),
    dirLabel: (dir) => dir || 'project root',
    refreshAndSelect: async (...args) => refreshed.push(args),
  }
  const uploadFiles = new Function(...Object.keys(dependencies), `${uploadFunctionJS}\nreturn uploadFiles`)(...Object.values(dependencies))
  return { uploadFiles, notifications, refreshed, writeBusy, writeStatus, projectContext }
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
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

test('offers delete only for a loaded selected file and cancels if its version changes during confirmation', async () => {
  const selection = {
    selectedPath: 'src/main.ts',
    content: null,
    loading: true,
    error: null,
  }
  const read = deferred()
  const opening = read.promise.then((content) => {
    selection.content = content
    selection.loading = false
  })

  assert.equal(selectedProjectFileDeleteVersion(selection), undefined)
  read.resolve({ path: 'src/main.ts', version: 'v1' })
  await opening
  const decisionVersion = selectedProjectFileDeleteVersion(selection)
  assert.equal(decisionVersion, 'v1')

  const confirmation = deferred()
  const deletion = confirmation.promise.then((confirmed) => confirmed
    && selection.selectedPath === 'src/main.ts'
    && selectedProjectFileDeleteVersion(selection) === decisionVersion
      ? decisionVersion
      : undefined)
  selection.content = { path: 'src/main.ts', version: 'v2' }
  confirmation.resolve(true)
  assert.equal(await deletion, undefined)

  selection.error = 'temporary read failure'
  assert.equal(selectedProjectFileDeleteVersion(selection), undefined)
  assert.notEqual(selectedProjectFileDeleteVersion(selection), decisionVersion)
  selection.error = null
  selection.selectedPath = 'README.md'
  selection.content = { path: 'README.md', version: 'v1' }
  assert.equal(selectedProjectFileDeleteVersion(selection), 'v1')
  assert.match(source, /:disabled="writesDisabled \|\| !selectedDeleteVersion"/)
  assert.match(source, /const version = selectedDeleteVersion\.value[\s\S]*selectedPath\.value !== path \|\| selectedDeleteVersion\.value !== version[\s\S]*ifMatch: version/)
})

test('continues independent uploads after an existing file baseline read fails', async () => {
  const attempts = []
  const harness = uploadHarness({
    async uploadProjectFiles(_ctx, _project, files, options) {
      const file = files[0]
      attempts.push({ name: file.name, dir: options.dir, overwrite: !!options.overwrite })
      if (file.name === 'existing.txt') throw { reason: 'exists', message: 'File already exists.' }
      return [{ path: `docs/${file.name}` }]
    },
    async readProjectFile(_ctx, _project, path) {
      attempts.push({ read: path })
      throw new Error('temporary read failure')
    },
  })
  await harness.uploadFiles([{ name: 'existing.txt' }, { name: 'later.txt' }], 'docs')

  assert.deepEqual(attempts, [
    { name: 'existing.txt', dir: 'docs', overwrite: false },
    { read: 'docs/existing.txt' },
    { name: 'later.txt', dir: 'docs', overwrite: false },
  ])
  assert.deepEqual(harness.notifications, [
    ['ok', 'Uploaded docs/later.txt.'],
    ['error', 'docs/existing.txt: temporary read failure'],
  ])
  assert.equal(harness.refreshed.length, 1)
  assert.deepEqual(harness.refreshed[0].slice(0, 2), ['docs/later.txt', 'shop'])
  assert.equal(harness.refreshed[0][2], harness.projectContext)
  assert.equal(harness.writeBusy.value, false)
  assert.equal(harness.writeStatus.value, '')
  assert.match(source, /if \(failures\.length === 1\) toast\('error', failures\[0\]\)/)
})

test('stops the upload batch when navigation changes during a failed baseline read', async () => {
  const attempted = []
  let current = true
  const harness = uploadHarness({
    current: () => current,
    async uploadProjectFiles(_ctx, _project, files) {
      attempted.push(files[0].name)
      throw { reason: 'exists', message: 'File already exists.' }
    },
    async readProjectFile() {
      current = false
      throw new Error('read failed during navigation')
    },
  })
  await harness.uploadFiles([{ name: 'existing.txt' }, { name: 'other-project.txt' }], '')

  assert.deepEqual(attempted, ['existing.txt'])
  assert.deepEqual(harness.notifications, [])
  assert.deepEqual(harness.refreshed, [])
})

test('advances the explorer revision only for an accepted terminal workspace mutation', () => {
  assert.match(appSource, /const codeExplorerRefreshRevision = ref\(0\)/)
  assert.match(appSource, /assistantRunTerminal\(normalized\.run\.status\) && acceptedTerminal[\s\S]*normalized\.message\.metadata\?\.previewRefreshNeeded === true[\s\S]*codeExplorerRefreshRevision\.value \+= 1/)
  assert.match(appSource, /<CodeExplorer[\s\S]*:refresh-revision="codeExplorerRefreshRevision"/)
})
