export interface DevelopmentPreviewRefreshControllerOptions<Project> {
  isMounted: () => boolean
  selectedProjectName: () => string | undefined
  getProject: (projectName: string) => Promise<Project>
  setSelectedProject: (project: Project) => void
}

interface InFlightAuthorization {
  key: string
  promise: Promise<unknown>
}

/**
 * Coordinates asynchronous project hydration and preview authorization for
 * the selected project. The callbacks keep this module independent of Vue,
 * while the controller owns the lifecycle and duplicate-request invariants.
 */
export class DevelopmentPreviewRefreshController<Project> {
  private projectRefreshSerial = 0
  private authorizationInFlight: InFlightAuthorization | null = null
  private disposed = false

  constructor(private readonly options: DevelopmentPreviewRefreshControllerOptions<Project>) {}

  isCurrent(projectName: string): boolean {
    return !this.disposed && this.options.isMounted() && this.options.selectedProjectName() === projectName
  }

  async hydrateProject(projectName: string): Promise<Project | undefined> {
    const serial = ++this.projectRefreshSerial
    if (!this.isCurrent(projectName)) return undefined

    let project: Project
    try {
      project = await this.options.getProject(projectName)
    } catch (error) {
      // A response that arrives after a project switch/unmount is no longer
      // relevant; do not surface it or mutate the replacement view.
      if (!this.isCurrent(projectName) || serial !== this.projectRefreshSerial) return undefined
      throw error
    }
    if (!this.isCurrent(projectName) || serial !== this.projectRefreshSerial) return undefined
    this.options.setSelectedProject(project)
    return project
  }

  async authorize<T>(projectName: string, key: string, request: () => Promise<T>): Promise<T | undefined> {
    if (!this.isCurrent(projectName)) return undefined
    const active = this.authorizationInFlight
    if (active?.key === key) return await active.promise as T

    const promise = request()
    const requestState: InFlightAuthorization = { key, promise }
    this.authorizationInFlight = requestState
    try {
      return await promise
    } finally {
      if (this.authorizationInFlight === requestState) this.authorizationInFlight = null
    }
  }

  invalidate() {
    this.projectRefreshSerial += 1
    this.authorizationInFlight = null
  }

  dispose() {
    this.disposed = true
    this.invalidate()
  }
}
