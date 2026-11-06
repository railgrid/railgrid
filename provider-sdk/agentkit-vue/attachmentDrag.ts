/** True when a drag carries OS files (not text or an in-page element). */
export function dragCarriesFiles(event: Pick<DragEvent, 'dataTransfer'>): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files')
}

/** Files from a drop. Folders are skipped (they arrive as zero-byte File objects). */
export function droppedFiles(dataTransfer: DataTransfer | null | undefined): { files: File[]; skippedFolders: number } {
  if (!dataTransfer) return { files: [], skippedFolders: 0 }
  const items = Array.from(dataTransfer.items ?? []).filter((item) => item.kind === 'file')
  if (items.length && items.every((item) => typeof item.webkitGetAsEntry === 'function')) {
    const files: File[] = []
    let skippedFolders = 0
    for (const item of items) {
      if (item.webkitGetAsEntry()?.isDirectory) {
        skippedFolders += 1
        continue
      }
      const file = item.getAsFile()
      if (file) files.push(file)
    }
    return { files, skippedFolders }
  }
  return { files: Array.from(dataTransfer.files ?? []), skippedFolders: 0 }
}
