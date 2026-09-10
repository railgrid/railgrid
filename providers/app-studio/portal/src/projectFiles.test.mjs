import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
test.after(async () => vite.close())

const files = await vite.ssrLoadModule('/src/projectFiles.ts')
const { api, isProjectFileRequestError } = await vite.ssrLoadModule('/src/api.ts')

globalThis.localStorage = {
  getItem: () => JSON.stringify({ orgUUID: 'org-1', workspaceUUID: 'ws-1' }),
  setItem: () => {},
  removeItem: () => {},
}

function stubContext(respond) {
  const calls = []
  const ctx = {
    basePath: '/ui/providers/app-studio',
    fetch: async (input, init = {}) => {
      calls.push({ url: String(input), init })
      return respond(String(input), init)
    },
  }
  return { ctx, calls }
}

function jsonResponse(status, body) {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

test('normalizes workspace paths and folders without escaping the workspace', () => {
  assert.equal(files.normalizeProjectFilePath(' ./public//assets/jeep.glb '), 'public/assets/jeep.glb')
  assert.equal(files.normalizeProjectFilePath('/src\\main.ts'), 'src/main.ts')
  assert.equal(files.normalizeProjectFilePath('../secret'), '')
  assert.equal(files.normalizeProjectFilePath('public/assets/'), '')
  assert.equal(files.normalizeProjectFilePath('   '), '')
  assert.equal(files.normalizeProjectFileDir(''), '')
  assert.equal(files.normalizeProjectFileDir('/public/assets/'), 'public/assets')
  assert.equal(files.normalizeProjectFileDir('public/../..'), null)
  assert.equal(files.joinProjectFilePath('', 'a.txt'), 'a.txt')
  assert.equal(files.joinProjectFilePath('public/assets', 'jeep.glb'), 'public/assets/jeep.glb')
  assert.equal(files.projectFileParentDir('public/assets/jeep.glb'), 'public/assets')
  assert.equal(files.projectFileParentDir('README.md'), '')
  assert.equal(files.projectFileBaseName('public/assets/jeep.glb'), 'jeep.glb')
})

test('enforces the 25 MiB file limit with a friendly message and formats sizes', () => {
  assert.equal(files.PROJECT_FILE_MAX_BYTES, 26214400)
  assert.equal(files.projectFileSizeError({ name: 'jeep.glb', size: 26214400 }), null)
  assert.match(files.projectFileSizeError({ name: 'jeep.glb', size: 26214401 }), /jeep\.glb is too large \(25 MiB\)\. Files must be 25 MiB or smaller\./)
  assert.equal(files.formatByteSize(512), '512 B')
  assert.equal(files.formatByteSize(1536), '1.5 KiB')
  assert.equal(files.formatByteSize(24 * 1024 * 1024), '24 MiB')
  assert.equal(files.projectFileHasImagePreview('logo.SVG'), true)
  assert.equal(files.projectFileHasImagePreview('photo.jpeg'), true)
  assert.equal(files.projectFileHasImagePreview('jeep.glb'), false)
})

test('classifies busy, exists, changed, and too-large responses', () => {
  const busy = 'wait for or stop the active assistant run before writing files'
  assert.equal(files.classifyProjectFileError(409, busy, {}), 'busy')
  assert.equal(files.classifyProjectFileError(409, busy, { upload: true }), 'busy')
  assert.equal(files.classifyProjectFileError(409, 'file public/a.png already exists', { upload: true }), 'exists')
  assert.equal(files.classifyProjectFileError(409, 'file "scripts/run.sh" already exists; upload with overwrite=true to replace it', { upload: true }), 'exists')
  assert.equal(files.classifyProjectFileError(409, 'file changed while it was being read; retry', {}), 'changed')
  assert.equal(files.classifyProjectFileError(412, 'file already exists', { createOnly: true }), 'exists')
  assert.equal(files.classifyProjectFileError(412, 'precondition failed', { ifMatch: 'sha256:abc' }), 'changed')
  assert.equal(files.classifyProjectFileError(412, 'file already exists', { upload: true }), 'exists')
  assert.equal(files.classifyProjectFileError(413, 'too big', {}), 'too-large')
  assert.equal(files.classifyProjectFileError(404, 'missing', {}), 'not-found')
  assert.equal(files.classifyProjectFileError(400, 'bad path', {}), 'invalid')
  assert.equal(files.classifyProjectFileError(500, 'boom', {}), 'other')
  assert.match(files.projectFileErrorMessage('busy', ''), /assistant is working/)
  assert.match(files.projectFileErrorMessage('too-large', ''), /25 MiB/)
})

test('file API routes use providerFetch with tenant headers and the agreed wire contract', async () => {
  const { ctx, calls } = stubContext((url, init) => {
    if (init.method === 'PUT') return jsonResponse(201, { path: 'notes.md', size: 0, version: 'sha256:00', binary: false })
    if (init.method === 'DELETE') return new Response(null, { status: 204 })
    if (url.includes('/files/upload')) return jsonResponse(200, { files: [{ path: 'public/assets/jeep.glb', size: 3, version: 'sha256:01', binary: true }] })
    if (url.includes('/files/raw')) return new Response(new Uint8Array([1, 2, 3]), { status: 200, headers: { 'Content-Type': 'model/gltf-binary' } })
    return jsonResponse(200, { path: 'a.txt', content: 'hi', binary: false })
  })

  const created = await api.putProjectFile(ctx, 'demo', 'notes.md', '', { createOnly: true })
  assert.deepEqual(created, { path: 'notes.md', size: 0, version: 'sha256:00', binary: false })
  assert.equal(calls[0].url, '/services/providers/app-studio/api/projects/demo/files/content?path=notes.md')
  assert.equal(calls[0].init.headers['If-None-Match'], '*')
  assert.equal(calls[0].init.headers['X-Faros-Org'], 'org-1')
  assert.equal(calls[0].init.headers['X-Faros-Workspace'], 'ws-1')
  assert.match(calls[0].init.headers['Content-Type'], /^text\/plain/)

  await api.putProjectFile(ctx, 'demo', 'a.bin', new Blob([new Uint8Array([0])]), { ifMatch: 'sha256:aa' })
  assert.equal(calls[1].init.headers['If-Match'], 'sha256:aa')
  assert.equal(calls[1].init.headers['If-None-Match'], undefined)
  assert.equal(calls[1].init.headers['Content-Type'], 'application/octet-stream')

  await api.deleteProjectFile(ctx, 'demo', 'public/a b.png', { ifMatch: 'sha256:bb' })
  assert.equal(calls[2].init.method, 'DELETE')
  assert.equal(calls[2].url, '/services/providers/app-studio/api/projects/demo/files/content?path=public%2Fa%20b.png')
  assert.equal(calls[2].init.headers['If-Match'], 'sha256:bb')

  const glb = new File([new Uint8Array([1, 2, 3])], 'jeep.glb', { type: 'model/gltf-binary' })
  const uploaded = await api.uploadProjectFiles(ctx, 'demo', [glb], { dir: 'public/assets', overwrite: true })
  assert.deepEqual(uploaded, [{ path: 'public/assets/jeep.glb', size: 3, version: 'sha256:01', binary: true }])
  assert.equal(calls[3].init.method, 'POST')
  assert.equal(calls[3].url, '/services/providers/app-studio/api/projects/demo/files/upload')
  const form = calls[3].init.body
  assert.ok(form instanceof FormData)
  assert.equal(form.getAll('file').length, 1)
  assert.equal(form.get('dir'), 'public/assets')
  assert.equal(form.get('overwrite'), 'true')
  assert.equal(calls[3].init.headers['Content-Type'], undefined, 'multipart boundary must come from the browser')

  const blob = await api.fetchProjectFileRaw(ctx, 'demo', 'public/assets/jeep.glb', { download: true })
  assert.equal(blob.size, 3)
  assert.equal(calls[4].url, '/services/providers/app-studio/api/projects/demo/files/raw?path=public%2Fassets%2Fjeep.glb&download=1')
})

test('file API maps 409, 412, and 413 to recoverable reasons', async () => {
  const cases = [
    [409, 'wait for or stop the active assistant run before writing files', 'busy'],
    [409, 'public/assets/jeep.glb already exists', 'exists'],
    [412, 'public/assets/jeep.glb already exists', 'exists'],
    [413, 'file exceeds the 26214400-byte limit', 'too-large'],
  ]
  for (const [status, message, reason] of cases) {
    const { ctx } = stubContext(() => jsonResponse(status, { message, reason: 'Conflict' }))
    const file = new File(['x'], 'jeep.glb')
    await assert.rejects(api.uploadProjectFiles(ctx, 'demo', [file], { dir: '' }), (error) => {
      assert.equal(isProjectFileRequestError(error), true)
      assert.equal(error.status, status)
      assert.equal(error.reason, reason)
      return true
    })
  }
  const { ctx } = stubContext(() => jsonResponse(412, { message: 'precondition failed' }))
  await assert.rejects(api.putProjectFile(ctx, 'demo', 'a.txt', 'x', { ifMatch: 'sha256:aa' }), (error) => error.reason === 'changed')
})

test('code explorer wires upload, drop, new file, delete, download, and binary preview', async () => {
  const source = await readFile(new URL('./CodeExplorer.vue', import.meta.url), 'utf8')
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(source, /aria-label="Upload files"[\s\S]*@click="openUploadPicker"/)
  assert.match(source, /aria-label="New file"[\s\S]*@click="openNewFile"/)
  assert.match(source, /type="file" class="hidden" multiple @change="handleUploadInput"/)
  assert.match(source, /@drop="handleTreeDrop"/)
  assert.match(source, /:data-drop-dir="row\.node\.dir \? row\.node\.path : projectFileParentDir\(row\.node\.path\)"/)
  assert.match(source, /api\.putProjectFile\(requestContext, projectName, path, '', \{ createOnly: true \}\)/)
  assert.match(source, /error\.reason === 'exists'[\s\S]*confirmDialog\([\s\S]*confirmLabel: 'Replace'[\s\S]*overwrite: true/)
  assert.match(source, /confirmDialog\(\{[\s\S]*title: `Delete \$\{projectFileBaseName\(path\)\}\?`[\s\S]*danger: true/)
  assert.match(source, /api\.fetchProjectFileRaw\(requestContext, projectName, path, \{ download: true \}\)[\s\S]*URL\.createObjectURL\(blob\)/)
  assert.match(source, /URL\.revokeObjectURL\(preview\.value\.url\)/)
  assert.match(source, /onBeforeUnmount\(releasePreview\)/)
  assert.match(source, /projectFileSizeError\(file\)/)
  assert.match(source, /File changes are paused while the assistant runs\./)
  assert.match(source, /No preview for this file type\./)
  assert.doesNotMatch(source, /Binary file — not shown\./)
  assert.match(app, /<CodeExplorer[\s\S]*:assistant-busy="messageStreaming"/)
})
