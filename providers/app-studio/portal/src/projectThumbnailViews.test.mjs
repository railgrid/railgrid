import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

let hydrationModulePromise
let requestTimeoutModulePromise

function loadHydrationModule() {
  if (!hydrationModulePromise) {
    hydrationModulePromise = (async () => {
      const source = await readFile(new URL('./projectThumbnailViews.ts', import.meta.url), 'utf8')
      const { outputText } = ts.transpileModule(source, {
        compilerOptions: {
          module: ts.ModuleKind.ES2022,
          target: ts.ScriptTarget.ES2022,
        },
      })
      const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
      return import(moduleURL)
    })()
  }
  return hydrationModulePromise
}

function loadRequestTimeoutModule() {
  if (!requestTimeoutModulePromise) {
    requestTimeoutModulePromise = (async () => {
      const source = await readFile(new URL('./requestTimeout.ts', import.meta.url), 'utf8')
      const { outputText } = ts.transpileModule(source, {
        compilerOptions: {
          module: ts.ModuleKind.ES2022,
          target: ts.ScriptTarget.ES2022,
        },
      })
      const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
      return import(moduleURL)
    })()
  }
  return requestTimeoutModulePromise
}

function project(name, overrides = {}) {
  return {
    name,
    uid: `${name}-uid`,
    displayName: `Project ${name}`,
    createdAt: '2026-10-23T00:00:00Z',
    ...overrides,
  }
}

function deferred() {
  let resolve
  const promise = new Promise((nextResolve) => {
    resolve = nextResolve
  })
  return { promise, resolve }
}

test('hydrates view metadata with bounded concurrency and keeps cards when a view fails', async () => {
  const { hydrateProjectThumbnailViews } = await loadHydrationModule()
  const projects = Array.from({ length: 9 }, (_, index) => project(`p${index}`))
  let active = 0
  let peak = 0
  const failures = []

  const hydrated = await hydrateProjectThumbnailViews(projects, async (name) => {
    active += 1
    peak = Math.max(peak, active)
    await new Promise((resolve) => setTimeout(resolve, 2))
    active -= 1
    if (name === 'p3') throw new Error('view unavailable')
    return project(name, {
      thumbnail: { available: true, revision: `${name}-digest` },
    })
  }, { onError: (name) => failures.push(name) })

  assert.equal(peak, 4)
  assert.equal(hydrated.length, projects.length)
  assert.equal(hydrated[0].thumbnail.revision, 'p0-digest')
  assert.equal(hydrated[3].name, projects[3].name)
  assert.equal(hydrated[3].thumbnail, undefined)
  assert.equal(hydrated[8].thumbnail.revision, 'p8-digest')
  assert.deepEqual(failures, ['p3'])
})

test('does not attach a view response to a same-name replacement project', async () => {
  const { hydrateProjectThumbnailViews } = await loadHydrationModule()
  const listed = project('demo', { thumbnail: { available: true, revision: 'old-digest' } })
  const reconciliations = []
  const [hydrated] = await hydrateProjectThumbnailViews([listed], async () => project('demo', {
    uid: 'replacement-uid',
    thumbnail: { available: true, revision: 'replacement-digest' },
  }), { onIdentityMismatch: (name) => reconciliations.push(name) })

  assert.equal(hydrated.uid, listed.uid)
  assert.deepEqual(hydrated.thumbnail, listed.thumbnail)
  assert.deepEqual(reconciliations, ['demo'])
})

test('publishes one ready thumbnail before a separate project view resolves', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  const projects = [project('ready'), project('stalled')]
  const stalled = deferred()
  const published = deferred()
  let current = true

  const hydration = hydrateProjectThumbnailGallery(projects, {
    concurrency: 2,
    isCurrent: () => current,
    getProjectView: async (name) => {
      if (name === 'stalled') return stalled.promise
      return project(name, { thumbnail: { available: true, revision: 'ready-digest' } })
    },
    getThumbnail: async () => new Blob(['image bytes']),
    onPublish: (item, revision, blob) => published.resolve({ name: item.name, revision, size: blob.size }),
  })

  assert.deepEqual(await published.promise, { name: 'ready', revision: 'ready-digest', size: 11 })
  current = false
  stalled.resolve(project('stalled'))
  await hydration
})

test('skips a thumbnail revision that is already committed for the project', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  let thumbnailRequests = 0
  const result = await hydrateProjectThumbnailGallery([project('cached')], {
    getProjectView: async () => project('cached', { thumbnail: { available: true, revision: 'same-digest' } }),
    shouldLoad: () => false,
    getThumbnail: async () => {
      thumbnailRequests += 1
      return new Blob(['duplicate image'])
    },
    onPublish: () => assert.fail('a cached revision must not be republished'),
  })

  assert.equal(thumbnailRequests, 0)
  assert.equal(result.shouldRetry, false)
})

test('a timed-out view releases its worker and reports a retryable failure', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  const projects = [project('slow'), project('healthy')]
  const requests = []

  const result = await hydrateProjectThumbnailGallery(projects, {
    concurrency: 1,
    getProjectView: async (name) => {
      requests.push(name)
      if (name === 'slow') {
        await new Promise((resolve) => setTimeout(resolve, 5))
        throw new Error('project view timed out')
      }
      return project(name, { thumbnail: { available: true, revision: 'healthy-digest' } })
    },
    getThumbnail: async () => new Blob(['healthy image']),
    onPublish: () => {},
  })

  assert.deepEqual(requests, ['slow', 'healthy'])
  assert.equal(result.projects[1].thumbnail.revision, 'healthy-digest')
  assert.equal(result.shouldRetry, true)
})

test('request timeout aborts the mocked transport signal', async () => {
  const { RequestTimeoutError, withRequestTimeout } = await loadRequestTimeoutModule()
  let observedSignal
  const mockedFetch = (_input, init) => new Promise((resolve, reject) => {
    observedSignal = init.signal
    init.signal.addEventListener('abort', () => reject(new Error('transport aborted')), { once: true })
  })

  await assert.rejects(
    withRequestTimeout(5, (signal) => mockedFetch('/project/view', { signal })),
    RequestTimeoutError,
  )
  assert.equal(observedSignal.aborted, true)
})

test('keeps a fast blob failure in retry bookkeeping while another view is still pending', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  const stalled = deferred()
  let failBlob
  const blobFailed = new Promise((resolve) => { failBlob = resolve })

  const hydration = hydrateProjectThumbnailGallery([project('broken-image'), project('slow-view')], {
    concurrency: 2,
    getProjectView: async (name) => {
      if (name === 'slow-view') return stalled.promise
      return project(name, { thumbnail: { available: true, revision: 'broken-digest' } })
    },
    getThumbnail: async () => {
      failBlob()
      throw new Error('thumbnail body timed out')
    },
    onPublish: () => assert.fail('failed image must not publish'),
  })

  await blobFailed
  stalled.resolve(project('slow-view'))
  const result = await hydration
  assert.equal(result.shouldRetry, true)
})

test('does not publish a thumbnail blob after its gallery guard becomes stale', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  const pending = deferred()
  let current = true
  let published = false

  const loading = hydrateProjectThumbnailGallery([project('demo')], {
    isCurrent: () => current,
    getProjectView: async () => project('demo', { thumbnail: { available: true, revision: 'digest' } }),
    getThumbnail: () => pending.promise,
    onPublish: () => { published = true },
  })

  current = false
  pending.resolve(new Blob(['late image']))
  await loading
  assert.equal(published, false)
})

test('stops applying view responses when the gallery context becomes stale', async () => {
  const { hydrateProjectThumbnailViews } = await loadHydrationModule()
  const projects = [project('first'), project('second')]
  const pending = deferred()
  let started
  const requestStarted = new Promise((resolve) => { started = resolve })
  let current = true
  let requests = 0

  const hydration = hydrateProjectThumbnailViews(projects, async () => {
    requests += 1
    started()
    return pending.promise
  }, { concurrency: 1, isCurrent: () => current })

  await requestStarted
  current = false
  pending.resolve(project('first', { thumbnail: { available: true, revision: 'stale' } }))

  assert.deepEqual(await hydration, projects)
  assert.equal(requests, 1)
})

test('the gallery obtains thumbnail metadata from per-project views before fetching bytes', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const api = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
  const hydrationStart = app.indexOf('async function hydrateProjectThumbnails(')
  const hydrationEnd = app.indexOf('\nasync function refreshProjectGalleryThumbnails()', hydrationStart)
  const galleryHydration = app.slice(hydrationStart, hydrationEnd)

  assert.match(api, /async listProjects\([\s\S]*projectKubeClient\(ctx\)\.list<ProjectCR>\(projectResource\)/)
  assert.match(api, /async getProject\([\s\S]*projectURL\(ctx, name, 'view'\)/)
  assert.match(app, /import \{ hydrateProjectThumbnailGallery \} from '\.\/projectThumbnailViews'/)
  assert.match(galleryHydration, /if \(!projectThumbnailRequestIsCurrent\(guard\)\) return/)
  assert.match(galleryHydration, /hydrateProjectThumbnailGallery\([\s\S]*projectList,[\s\S]*getProjectView: \(name\) => api\.getProject\(guard\.ctx, name, \{[\s\S]*timeoutMS: PROJECT_THUMBNAIL_VIEW_TIMEOUT_MS/)
  assert.match(galleryHydration, /getThumbnail: \(project, revision\) => projectThumbnailBlobRequest\(project, revision, guard\)/)
  assert.match(galleryHydration, /onPublish: \(currentProject, publishedRevision, blob\) => \{[\s\S]*URL\.createObjectURL\(blob\)/)
  assert.match(app, /api\.getProjectThumbnail\(guard\.ctx, project\.name, revision, controller\.signal\)/)
  assert.match(app, /controller\.abort\(\)/)
  assert.match(api, /async getProject\([\s\S]*timeoutMS\?: number[\s\S]*request<Project>\([\s\S]*undefined, options\)/)
  assert.match(api, /async getProjectThumbnail\([\s\S]*signal\?: AbortSignal[\s\S]*requestBlob\([\s\S]*signal\)/)
  assert.match(api, /withRequestTimeout\(options\.timeoutMS, async \(signal\) => \{[\s\S]*providerFetch\(ctx\)\(path, \{[\s\S]*signal,/)
  assert.match(api, /error instanceof RequestTimeoutError/)
  assert.match(app, /PROJECT_THUMBNAIL_REFRESH_MAX_DELAY_MS = 30_000/)
  assert.match(app, /PROJECT_THUMBNAIL_REFRESH_MAX_WINDOW_MS = 5 \* 60_000/)
  assert.match(app, /hasProjectRequestTransport\(props\.ctx\)/)
  assert.match(app, /projectThumbnailProjectUIDs = new Map<string, string>\(\)/)
  assert.match(galleryHydration, /cachedUID !== undefined && cachedUID !== uid/)
})
