// Pure helpers for the Code tab's workspace file writes. The server remains
// the authority for every limit; these mirror the shared binary-file contract
// so the browser can refuse an obviously oversized file before uploading it.

/** Per-file ceiling for workspace writes (decoded bytes). */
export const PROJECT_FILE_MAX_BYTES = 25 * 1024 * 1024

const IMAGE_PREVIEW_EXTENSIONS = /\.(?:png|jpe?g|gif|webp|svg)$/iu

export type ProjectFileErrorReason = 'busy' | 'exists' | 'changed' | 'too-large' | 'not-found' | 'invalid' | 'other'

export interface ProjectFileWriteIntent {
  /** PUT with If-None-Match: * (create only). */
  createOnly?: boolean
  /** PUT/DELETE with If-Match: <version>. */
  ifMatch?: string
  /** Multipart upload without overwrite=true. */
  upload?: boolean
}

/** Human byte size for file metadata ("24.8 MB" style, binary units). */
export function formatByteSize(bytes: number | undefined | null): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return ''
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value >= 10 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`
}

export function projectFileHasImagePreview(path: string): boolean {
  return IMAGE_PREVIEW_EXTENSIONS.test(path.trim())
}

export function projectFileBaseName(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? path
}

export function projectFileParentDir(path: string): string {
  const index = path.lastIndexOf('/')
  return index > 0 ? path.slice(0, index) : ''
}

/**
 * Normalize a user-typed workspace path: trim, use forward slashes, drop
 * leading "./" and "/" and collapse duplicate separators. Returns '' when the
 * path is unusable (empty, a directory, or escapes the workspace with "..").
 */
export function normalizeProjectFilePath(raw: string): string {
  const cleaned = raw.trim().replace(/\\/gu, '/')
  if (!cleaned || cleaned.endsWith('/')) return ''
  const segments = cleaned.split('/').filter((segment) => segment !== '' && segment !== '.')
  if (!segments.length || segments.some((segment) => segment === '..')) return ''
  return segments.join('/')
}

/** Normalize a target directory; '' is the workspace root. Returns null when invalid. */
export function normalizeProjectFileDir(raw: string): string | null {
  const cleaned = raw.trim().replace(/\\/gu, '/')
  const segments = cleaned.split('/').filter((segment) => segment !== '' && segment !== '.')
  if (segments.some((segment) => segment === '..')) return null
  return segments.join('/')
}

export function joinProjectFilePath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

/** True when a drag carries OS files (not text or an in-page element). */
export function dragCarriesFiles(event: Pick<DragEvent, 'dataTransfer'>): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files')
}

/**
 * Files from a drop. Folders are skipped (they arrive as zero-byte File
 * objects) and counted so the caller can say so.
 */
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

/** Returns a friendly error when a picked file exceeds the per-file limit. */
export function projectFileSizeError(file: Pick<File, 'name' | 'size'>): string | null {
  if (file.size > PROJECT_FILE_MAX_BYTES) {
    return `${file.name || 'This file'} is too large (${formatByteSize(file.size)}). Files must be 25 MiB or smaller.`
  }
  return null
}

/**
 * Classify a failed workspace file request. 409 is shared by the assistant
 * run reservation and (for uploads) an existing file, so the server message
 * disambiguates; 412 is always a precondition (exists or changed).
 */
export function classifyProjectFileError(status: number, detail: string, intent: ProjectFileWriteIntent = {}): ProjectFileErrorReason {
  const message = detail.toLowerCase()
  if (status === 413) return 'too-large'
  if (status === 404) return 'not-found'
  if (status === 400) return 'invalid'
  // Match the server's own 409 wording before the busy heuristic: the path in
  // "file \"scripts/run.sh\" already exists" must not read as an active run.
  if (status === 409 && /already exists/u.test(message)) return 'exists'
  if (status === 409 && /file changed while/u.test(message)) return 'changed'
  if (status === 409 && /assistant|run\b/u.test(message)) return 'busy'
  if (status === 412 || (status === 409 && intent.upload)) {
    if (intent.ifMatch && !intent.createOnly) return 'changed'
    if (/changed|mismatch|version/u.test(message) && !/exist/u.test(message)) return 'changed'
    return 'exists'
  }
  if (status === 409) return 'busy'
  return 'other'
}

export function projectFileErrorMessage(reason: ProjectFileErrorReason, detail: string): string {
  switch (reason) {
    case 'busy':
      return 'The assistant is working on this project. Try again when the run finishes.'
    case 'exists':
      return 'A file already exists at that path.'
    case 'changed':
      return 'The file changed since it was loaded. Refresh and try again.'
    case 'too-large':
      return 'The file is too large. Files must be 25 MiB or smaller.'
    case 'not-found':
      return 'The file no longer exists. Refresh the file list.'
    case 'invalid':
      return detail || 'The file path is not valid.'
    default:
      return detail || 'The file request failed.'
  }
}
