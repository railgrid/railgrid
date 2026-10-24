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

async function loadAppThumbnailHydrator(dependencies) {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const start = app.indexOf('async function hydrateProjectThumbnails(')
  const end = app.indexOf('\nfunction projectThumbnailBlobRequest(', start)
  const { outputText } = ts.transpileModule(app.slice(start, end), {
    compilerOptions: {
      module: ts.ModuleKind.None,
      target: ts.ScriptTarget.ES2022,
    },
  })
  const buildModule = new Function('dependencies', `
    const {
      api,
      hydrateProjectThumbnailGallery,
      selectProjectThumbnailRetryTargets,
      projectThumbnailProjectUIDs,
      projectThumbnailURLs,
      projectThumbnailRevisions,
      projectThumbnailRequestIsCurrent,
      removeProjectThumbnail,
      projectThumbnailBlobRequest,
      projects,
      updateProjectThumbnailRefresh,
      PROJECT_THUMBNAIL_VIEW_TIMEOUT_MS,
    } = dependencies
    let projectThumbnailRetryNames = dependencies.projectThumbnailRetryNames
    ${outputText}
    return {
      hydrateProjectThumbnails,
      retryNames: () => projectThumbnailRetryNames,
    }
  `)
  return buildModule(dependencies)
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
  assert.deepEqual(result.retryNames, [])
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
  assert.deepEqual(result.retryNames, ['slow'])
})

test('shared request timeout aborts the mocked thumbnail transport signal', async () => {
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
  assert.deepEqual(result.retryNames, ['broken-image'])
})

test('retry selection excludes healthy cards and reconciles recovery, deletion, and replacement', async () => {
  const { hydrateProjectThumbnailGallery, selectProjectThumbnailRetryTargets } = await loadHydrationModule()
  const listed = [
    project('healthy'),
    project('retrying'),
    project('recovered'),
    project('replaced', { uid: 'replacement-uid' }),
    project('new'),
  ]
  const retryNames = new Set(['retrying', 'recovered', 'deleted'])
  const knownUIDs = new Map([
    ['healthy', 'healthy-uid'],
    ['retrying', 'retrying-uid'],
    ['recovered', 'recovered-uid'],
    ['deleted', 'deleted-uid'],
    ['replaced', 'old-uid'],
  ])

  const selection = selectProjectThumbnailRetryTargets(listed, retryNames, knownUIDs, true)
  assert.deepEqual([...selection.targetNames], ['retrying', 'recovered', 'replaced', 'new'])
  assert.deepEqual(selection.deletedNames, ['deleted'])
  assert.deepEqual(selection.replacedNames, ['replaced'])
  assert.equal(selection.targetNames.has('healthy'), false)

  const recovered = await hydrateProjectThumbnailGallery(
    listed.filter((item) => selection.targetNames.has(item.name)),
    {
      getProjectView: async (name) => {
        const listedProject = listed.find((item) => item.name === name)
        return project(name, {
          uid: listedProject.uid,
          thumbnail: { available: true, revision: `${name}-digest` },
        })
      },
      getThumbnail: async () => new Blob(['recovered thumbnail']),
      onPublish: () => {},
    },
  )
  assert.deepEqual(recovered.retryNames, [])

  for (const name of selection.deletedNames) knownUIDs.delete(name)
  for (const item of listed) knownUIDs.set(item.name, item.uid)
  const nextRetry = selectProjectThumbnailRetryTargets(listed, new Set(recovered.retryNames), knownUIDs, true)
  assert.deepEqual([...nextRetry.targetNames], [])
  assert.deepEqual(nextRetry.deletedNames, [])
})

test('a retry pass inherits targets from an overlapping full pass that becomes stale', async () => {
  const { hydrateProjectThumbnailGallery, selectProjectThumbnailRetryTargets } = await loadHydrationModule()
  const listed = [
    project('healthy'),
    project('replaced', { uid: 'replacement-uid' }),
    project('new'),
  ]
  const identities = new Map(listed.map((item) => [item.name, item.uid]))
  const passA = new Map(listed.map((item) => [item.name, deferred()]))
  const passB = new Map(listed.map((item) => [item.name, deferred()]))
  const requestCounts = new Map()
  const calls = []
  const revisions = new Map([['healthy', 'healthy-digest'], ['replaced', 'old-digest']])
  const imageURLs = { healthy: 'blob:healthy', replaced: 'blob:old' }
  const thumbnailUIDs = new Map([
    ['healthy', 'healthy-uid'],
    ['replaced', 'old-uid'],
  ])
  let currentSerial = 1
  const retryState = new Set()
  const hydrate = await loadAppThumbnailHydrator({
    api: {
      getProject: (_ctx, name) => {
        calls.push(name)
        const count = (requestCounts.get(name) ?? 0) + 1
        requestCounts.set(name, count)
        return (count === 1 ? passA : passB).get(name).promise
      },
    },
    hydrateProjectThumbnailGallery,
    selectProjectThumbnailRetryTargets,
    projectThumbnailProjectUIDs: thumbnailUIDs,
    projectThumbnailURLs: { value: imageURLs },
    projectThumbnailRevisions: revisions,
    projectThumbnailRetryNames: retryState,
    projectThumbnailRequestIsCurrent: (guard) => guard.serial === currentSerial,
    removeProjectThumbnail: (name) => {
      delete imageURLs[name]
      revisions.delete(name)
      thumbnailUIDs.delete(name)
    },
    projectThumbnailBlobRequest: async () => new Blob(),
    projects: { value: listed },
    updateProjectThumbnailRefresh: () => {},
    PROJECT_THUMBNAIL_VIEW_TIMEOUT_MS: 15_000,
  })

  const fullPass = hydrate.hydrateProjectThumbnails(listed, { serial: 1, ctx: {} })
  assert.deepEqual([...hydrate.retryNames()], ['healthy', 'replaced', 'new'])
  assert.deepEqual(calls, ['healthy', 'replaced', 'new'])

  currentSerial = 2
  const retryPass = hydrate.hydrateProjectThumbnails(listed, { serial: 2, ctx: {} }, true)
  assert.deepEqual(calls.slice(3), ['healthy', 'replaced', 'new'])

  for (const item of listed) {
    const view = project(item.name, {
      uid: identities.get(item.name),
      thumbnail: { available: false },
    })
    passA.get(item.name).resolve(view)
  }
  await fullPass
  assert.deepEqual([...hydrate.retryNames()], ['healthy', 'replaced', 'new'])

  for (const item of listed) {
    const view = project(item.name, {
      uid: identities.get(item.name),
      thumbnail: { available: false },
    })
    passB.get(item.name).resolve(view)
  }
  await retryPass
  assert.deepEqual([...hydrate.retryNames()], [])
})

test('full gallery hydration selects every project regardless of retry state', async () => {
  const { selectProjectThumbnailRetryTargets } = await loadHydrationModule()
  const projects = [project('healthy'), project('failed')]
  const selection = selectProjectThumbnailRetryTargets(
    projects,
    new Set(['failed']),
    new Map(projects.map((item) => [item.name, item.uid])),
    false,
  )

  assert.deepEqual([...selection.targetNames], ['healthy', 'failed'])
})

test('a view identity mismatch remains retryable without fetching or publishing bytes', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  const listed = project('demo')
  let thumbnailRequests = 0
  const result = await hydrateProjectThumbnailGallery([listed], {
    getProjectView: async () => project('demo', {
      uid: 'replacement-uid',
      thumbnail: { available: true, revision: 'replacement-digest' },
    }),
    getThumbnail: async () => {
      thumbnailRequests += 1
      return new Blob(['must not fetch'])
    },
    onPublish: () => assert.fail('a mismatched identity must not publish'),
  })

  assert.deepEqual(result.retryNames, ['demo'])
  assert.equal(thumbnailRequests, 0)
})

test('refreshing metadata preserves the current image until a ready revision arrives', async () => {
  const { hydrateProjectThumbnailGallery } = await loadHydrationModule()
  const imageURLs = { healthy: 'blob:healthy', refreshing: 'blob:previous' }
  let thumbnailRequests = 0
  const refreshing = await hydrateProjectThumbnailGallery([project('refreshing')], {
    getProjectView: async () => project('refreshing', {
      thumbnail: { available: true, refreshing: true, revision: 'previous-digest' },
    }),
    shouldLoad: () => false,
    getThumbnail: async () => {
      thumbnailRequests += 1
      return new Blob(['unexpected replacement'])
    },
    onPublish: () => assert.fail('the cached revision must not be republished'),
  })

  assert.deepEqual(refreshing.retryNames, ['refreshing'])
  assert.equal(thumbnailRequests, 0)
  assert.deepEqual(imageURLs, { healthy: 'blob:healthy', refreshing: 'blob:previous' })

  const ready = await hydrateProjectThumbnailGallery([project('refreshing')], {
    getProjectView: async () => project('refreshing', {
      thumbnail: { available: true, revision: 'ready-digest' },
    }),
    shouldLoad: (_item, revision) => revision !== 'previous-digest',
    getThumbnail: async () => {
      thumbnailRequests += 1
      return new Blob(['ready replacement'])
    },
    onPublish: (_item, revision) => { imageURLs.refreshing = `blob:${revision}` },
  })

  assert.deepEqual(ready.retryNames, [])
  assert.equal(thumbnailRequests, 1)
  assert.deepEqual(imageURLs, { healthy: 'blob:healthy', refreshing: 'blob:ready-digest' })
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
  assert.match(app, /import \{[\s\S]*hydrateProjectThumbnailGallery,[\s\S]*selectProjectThumbnailRetryTargets,[\s\S]*\} from '\.\/projectThumbnailViews'/)
  assert.match(galleryHydration, /if \(!projectThumbnailRequestIsCurrent\(guard\)\) return/)
  assert.match(galleryHydration, /hydrateProjectThumbnailGallery\([\s\S]*targetProjects,[\s\S]*getProjectView: \(name\) => api\.getProject\(guard\.ctx, name, \{[\s\S]*timeoutMS: PROJECT_THUMBNAIL_VIEW_TIMEOUT_MS/)
  assert.match(galleryHydration, /getThumbnail: \(project, revision\) => projectThumbnailBlobRequest\(project, revision, guard\)/)
  assert.match(galleryHydration, /onPublish: \(currentProject, publishedRevision, blob\) => \{[\s\S]*URL\.createObjectURL\(blob\)/)
  const blobRequestStart = app.indexOf('function projectThumbnailBlobRequest(')
  const blobRequestEnd = app.indexOf('\nasync function refreshProjectGalleryThumbnails()', blobRequestStart)
  const blobRequest = app.slice(blobRequestStart, blobRequestEnd)
  assert.match(api, /async getProject\([\s\S]*timeoutMS\?: number[\s\S]*request<Project>\([\s\S]*undefined, options\)/)
  assert.match(api, /async getProjectThumbnail\([\s\S]*signal\?: AbortSignal[\s\S]*requestBlob\([\s\S]*signal\)/)
  assert.match(blobRequest, /withRequestTimeout\(\s*PROJECT_THUMBNAIL_BLOB_TIMEOUT_MS,\s*\(signal\) => api\.getProjectThumbnail\(guard\.ctx, project\.name, revision, signal\),\s*\)/)
  assert.doesNotMatch(blobRequest, /new AbortController|setTimeout/)
  assert.match(api, /withRequestTimeout\(options\.timeoutMS, async \(signal\) => \{[\s\S]*providerFetch\(ctx\)\(path, \{[\s\S]*signal,/)
  assert.match(api, /error instanceof RequestTimeoutError/)
  assert.match(app, /PROJECT_THUMBNAIL_REFRESH_MAX_DELAY_MS = 30_000/)
  assert.match(app, /PROJECT_THUMBNAIL_REFRESH_MAX_WINDOW_MS = 5 \* 60_000/)
  assert.match(app, /hasProjectRequestTransport\(props\.ctx\)/)
  assert.match(app, /projectThumbnailProjectUIDs = new Map<string, string>\(\)/)
  assert.match(galleryHydration, /selectProjectThumbnailRetryTargets\([\s\S]*projectList,[\s\S]*projectThumbnailRetryNames,[\s\S]*projectThumbnailProjectUIDs,[\s\S]*retryOnly/)
  assert.match(galleryHydration, /for \(const name of cachedNames\)[\s\S]*if \(liveNames\.has\(name\)\) continue[\s\S]*const targetProjects = retryOnly[\s\S]*projectList\.filter\(\(project\) => targetNames\.has\(project\.name\)\)/)
  assert.match(galleryHydration, /if \(!projectThumbnailRequestIsCurrent\(guard\)\) return\s*\n\s*projectThumbnailRetryNames = new Set\(retryNames\)/)
  assert.match(galleryHydration, /projectThumbnailRetryNames\.delete\(name\)/)
  assert.match(galleryHydration, /for \(const name of replacedNames\) removeProjectThumbnail\(name\)/)
  assert.match(app, /await hydrateProjectThumbnails\(visibleProjectList, guard, true\)/)
})
