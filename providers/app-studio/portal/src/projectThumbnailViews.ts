import type { Project } from './types'

export interface ProjectThumbnailViewHydrationOptions {
  concurrency?: number
  isCurrent?: () => boolean
  onError?: (projectName: string) => void
  onIdentityMismatch?: (projectName: string) => void
  /** Called as soon as one view resolves; callbacks must not block the worker pool. */
  onView?: (project: Project, view: Project) => void
}

export interface ProjectThumbnailImageHydrationOptions {
  isCurrent?: () => boolean
  shouldLoad?: (project: Project, revision: string) => boolean
  getThumbnail: (revision: string) => Promise<Blob>
  onPublish: (project: Project, revision: string, blob: Blob) => void
  onError?: (projectName: string) => void
}

export interface ProjectThumbnailGalleryHydrationOptions {
  concurrency?: number
  isCurrent?: () => boolean
  shouldLoad?: (project: Project, revision: string) => boolean
  getProjectView: (name: string) => Promise<Project>
  getThumbnail: (project: Project, revision: string) => Promise<Blob>
  onPublish: (project: Project, revision: string, blob: Blob) => void
}

export interface ProjectThumbnailGalleryHydrationResult {
  projects: Project[]
  shouldRetry: boolean
}

const DEFAULT_CONCURRENCY = 4

/**
 * Fetch and publish one thumbnail independently from the other gallery cards.
 * The caller's currentness guard runs again after the body is read so a late
 * response cannot mutate the cache for a route or project that has changed.
 */
export async function hydrateProjectThumbnailImage(
  project: Project,
  view: Project,
  options: ProjectThumbnailImageHydrationOptions,
): Promise<void> {
  const revision = view.thumbnail?.revision ?? ''
  const isCurrent = options.isCurrent ?? (() => true)
  if (!view.thumbnail?.available || !revision || !isCurrent()) return
  if (options.shouldLoad && !options.shouldLoad(project, revision)) return
  try {
    const blob = await options.getThumbnail(revision)
    if (!isCurrent()) return
    options.onPublish(project, revision, blob)
  } catch {
    if (isCurrent()) options.onError?.(project.name)
  }
}

/**
 * Hydrate the gallery and start each image request as soon as its view arrives.
 * Image callbacks publish independently; this function waits for bounded image
 * work only before returning retry bookkeeping to the caller.
 */
export async function hydrateProjectThumbnailGallery(
  projects: Project[],
  options: ProjectThumbnailGalleryHydrationOptions,
): Promise<ProjectThumbnailGalleryHydrationResult> {
  let requestFailed = false
  const imageLoads: Promise<void>[] = []
  const isCurrent = options.isCurrent ?? (() => true)
  const hydrated = await hydrateProjectThumbnailViews(projects, options.getProjectView, {
    concurrency: options.concurrency,
    isCurrent,
    onError: () => { requestFailed = true },
    onIdentityMismatch: () => { requestFailed = true },
    onView: (project, view) => {
      imageLoads.push(hydrateProjectThumbnailImage(project, view, {
        isCurrent,
        shouldLoad: options.shouldLoad,
        getThumbnail: (revision) => options.getThumbnail(project, revision),
        onPublish: options.onPublish,
        onError: () => { requestFailed = true },
      }))
    },
  })
  await Promise.all(imageLoads)
  return {
    projects: hydrated,
    shouldRetry: requestFailed || hydrated.some((project) => project.thumbnail?.refreshing),
  }
}

/**
 * The workspace Project list contains bound CRs, while thumbnail metadata is
 * added by the provider's `view` action. Hydrate those views with a fixed
 * worker pool so a large gallery does not fan out one request per card at once.
 */
export async function hydrateProjectThumbnailViews(
  projects: Project[],
  getProjectView: (name: string) => Promise<Project>,
  options: ProjectThumbnailViewHydrationOptions = {},
): Promise<Project[]> {
  const hydrated = [...projects]
  const isCurrent = options.isCurrent ?? (() => true)
  const concurrency = Number.isFinite(options.concurrency)
    ? Math.max(1, Math.floor(options.concurrency ?? DEFAULT_CONCURRENCY))
    : DEFAULT_CONCURRENCY
  let nextIndex = 0

  const worker = async () => {
    while (isCurrent()) {
      const index = nextIndex++
      if (index >= projects.length) return

      const project = projects[index]
      let view: Project
      try {
        view = await getProjectView(project.name)
      } catch {
        // A failed view read should leave this card in the gallery. The caller
        // can schedule a bounded retry without changing the list projection.
        options.onError?.(project.name)
        continue
      }

      if (!isCurrent()) return
      if (project.uid && view.uid && project.uid !== view.uid) {
        options.onIdentityMismatch?.(project.name)
        continue
      }
      hydrated[index] = { ...project, thumbnail: view.thumbnail }
      options.onView?.(project, view)
    }
  }

  const workerCount = Math.min(concurrency, projects.length)
  await Promise.all(Array.from({ length: workerCount }, () => worker()))
  return hydrated
}
