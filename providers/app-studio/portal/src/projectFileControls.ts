export interface SelectedProjectFileSnapshot {
  selectedPath: string
  content: { path: string; version?: string } | null
  loading: boolean
  error: string | null
}

/** Return the exact loaded version that may be used to delete the selection. */
export function selectedProjectFileDeleteVersion(snapshot: SelectedProjectFileSnapshot): string | undefined {
  if (snapshot.loading || snapshot.error || !snapshot.selectedPath) return undefined
  if (snapshot.content?.path !== snapshot.selectedPath || !snapshot.content.version) return undefined
  return snapshot.content.version
}
