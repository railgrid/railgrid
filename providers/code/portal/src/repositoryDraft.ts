// In-memory handoff for the connection prerequisite; never includes credentials.
export interface RepositoryDraft {
  name: string
  repo: string
  connectionRef: string
  visibility: string
  description: string
  autoInit: boolean
}
