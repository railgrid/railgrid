<script lang="ts">
export type CodeExplorerTreeState =
  | 'initial-loading'
  | 'initial-error'
  | 'refreshing'
  | 'refresh-error'
  | 'empty'
  | 'ready'

/**
 * Keep a cached tree visible while a refresh is in flight or has failed. The
 * initial load is the only state where loading/error replaces the tree body.
 */
export function codeExplorerTreeState(
  loading: boolean,
  hasFiles: boolean,
  error: string | null,
): CodeExplorerTreeState {
  if (error && hasFiles) return 'refresh-error'
  if (error) return 'initial-error'
  if (loading && hasFiles) return 'refreshing'
  if (loading) return 'initial-loading'
  if (hasFiles) return 'ready'
  return 'empty'
}
</script>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import {
  ChevronLeft,
  Download,
  RefreshCw,
  Folder,
  FolderOpen,
  File as FileIcon,
  FilePlus,
  Image as ImageIcon,
  Loader2,
  Trash2,
  Upload,
} from 'lucide-vue-next'
import type { FarosContext, ProjectFileInfo, ProjectFileContent, ProjectFileWriteResult } from './types'
import { api, isProjectFileRequestError } from './api'
import { confirmDialog } from './portalkit/confirm'
import { toast } from './portalkit/toast'
import {
  dragCarriesFiles,
  droppedFiles,
  formatByteSize,
  joinProjectFilePath,
  normalizeProjectFileDir,
  normalizeProjectFilePath,
  projectFileBaseName,
  projectFileHasImagePreview,
  projectFileParentDir,
  projectFileSizeError,
} from './projectFiles'

// Explorer of the live development workspace — the same files the assistant
// edits and development sync pushes into the sandbox. Left: a collapsible tree
// built from the flat path list, with upload, new-file, and drop targets.
// Right: the selected file's content, or metadata, preview, and download for
// binary files. Writes are paused while an assistant run owns the project.

const props = withDefaults(defineProps<{
  ctx: FarosContext | null
  projectName: string
  refreshRevision: number
  /** An assistant run owns the workspace; the server answers writes with 409. */
  assistantBusy?: boolean
}>(), {
  assistantBusy: false,
})

const files = ref<ProjectFileInfo[]>([])
const loadingTree = ref(false)
const treeError = ref<string | null>(null)
let treeRequestSerial = 0

const selectedPath = ref<string>('')
const content = ref<ProjectFileContent | null>(null)
const loadingFile = ref(false)
const fileError = ref<string | null>(null)
let fileRequestSerial = 0
let workspaceRefreshSerial = 0

const collapsed = ref<Set<string>>(new Set())
const mobileTreeOpen = ref(true)
const activeTreePath = ref('')
const rootRef = ref<HTMLElement | null>(null)
const treeRootRef = ref<HTMLElement | null>(null)
const mobileBackRef = ref<HTMLButtonElement | null>(null)
const uploadInputRef = ref<HTMLInputElement | null>(null)
const newFileInputRef = ref<HTMLInputElement | null>(null)
const uploadDirInputRef = ref<HTMLInputElement | null>(null)

// Write state. One write sequence runs at a time; writeStatus names it.
const writeBusy = ref(false)
const writeStatus = ref('')
const downloadBusy = ref(false)
const uploadDraft = ref<{ files: File[]; dir: string } | null>(null)
const uploadDraftError = ref('')
const newFileOpen = ref(false)
const newFilePath = ref('')
const newFileError = ref('')
const dropTargetDir = ref<string | null>(null)
let dragDepth = 0

// Image preview for the selected file, loaded from files/raw as a blob.
const preview = ref<{ status: 'idle' | 'loading' | 'ready' | 'error'; url: string; path: string }>({ status: 'idle', url: '', path: '' })
const showImageSource = ref(false)
let previewController: AbortController | null = null

interface TreeNode {
  name: string
  path: string
  dir: boolean
  size?: number
  children: TreeNode[]
}

// Build a nested tree from sorted flat paths. Directories are inferred from
// path segments; files are leaves.
const tree = computed<TreeNode[]>(() => {
  const root: TreeNode = { name: '', path: '', dir: true, children: [] }
  const dirIndex = new Map<string, TreeNode>([['', root]])
  const sorted = [...files.value].sort((a, b) => a.path.localeCompare(b.path))
  for (const f of sorted) {
    const parts = f.path.split('/')
    let parentPath = ''
    for (let i = 0; i < parts.length; i++) {
      const isLeaf = i === parts.length - 1
      const segPath = parentPath ? `${parentPath}/${parts[i]}` : parts[i]
      if (isLeaf) {
        dirIndex.get(parentPath)!.children.push({ name: parts[i], path: segPath, dir: false, size: f.size, children: [] })
      } else if (!dirIndex.has(segPath)) {
        const node: TreeNode = { name: parts[i], path: segPath, dir: true, children: [] }
        dirIndex.get(parentPath)!.children.push(node)
        dirIndex.set(segPath, node)
      }
      parentPath = segPath
    }
  }
  const sortNodes = (nodes: TreeNode[]): TreeNode[] => {
    nodes.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1))
    for (const n of nodes) if (n.dir) sortNodes(n.children)
    return nodes
  }
  return sortNodes(root.children)
})

// Flatten the tree into visible rows (respecting collapsed dirs) with depth.
interface Row {
  node: TreeNode
  depth: number
  posInSet: number
  setSize: number
}
const rows = computed<Row[]>(() => {
  const out: Row[] = []
  const walk = (nodes: TreeNode[], depth: number) => {
    for (const [index, n] of nodes.entries()) {
      out.push({ node: n, depth, posInSet: index + 1, setSize: nodes.length })
      if (n.dir && !collapsed.value.has(n.path)) walk(n.children, depth + 1)
    }
  }
  walk(tree.value, 0)
  return out
})

const directoryPaths = computed(() => {
  const dirs = new Set<string>()
  for (const file of files.value) {
    let parent = projectFileParentDir(file.path)
    while (parent) {
      dirs.add(parent)
      parent = projectFileParentDir(parent)
    }
  }
  return dirs
})

/** The folder new uploads and files default to: the focused folder, or the focused file's folder. */
const contextDir = computed(() => {
  const active = activeTreePath.value || selectedPath.value
  if (!active) return ''
  return directoryPaths.value.has(active) ? active : projectFileParentDir(active)
})

const writesDisabled = computed(() => !props.projectName || props.assistantBusy || writeBusy.value)

function dirLabel(dir: string): string {
  return dir || 'project root'
}

function toggleDir(path: string) {
  const next = new Set(collapsed.value)
  if (next.has(path)) next.delete(path)
  else next.add(path)
  collapsed.value = next
}

function selectRow(row: Row) {
  activeTreePath.value = row.node.path
  if (row.node.dir) {
    toggleDir(row.node.path)
    return
  }
  mobileTreeOpen.value = false
  void openFile(row.node.path)
  if (typeof window !== 'undefined' && window.matchMedia('(max-width: 767px)').matches) {
    void nextTick(() => mobileBackRef.value?.focus())
  }
}

function showMobileTree() {
  mobileTreeOpen.value = true
  const activeIndex = Math.max(0, rows.value.findIndex((row) => row.node.path === activeTreePath.value))
  focusTreeRow(activeIndex)
}

function focusTreeRow(index: number) {
  const row = rows.value[index]
  if (!row) return
  activeTreePath.value = row.node.path
  void nextTick(() => {
    treeRootRef.value?.querySelectorAll<HTMLElement>('[role="treeitem"]')[index]?.focus()
  })
}

function handleTreeKeydown(event: KeyboardEvent, row: Row, index: number) {
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    focusTreeRow(Math.min(rows.value.length - 1, index + 1))
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    focusTreeRow(Math.max(0, index - 1))
  } else if (event.key === 'Home') {
    event.preventDefault()
    focusTreeRow(0)
  } else if (event.key === 'End') {
    event.preventDefault()
    focusTreeRow(rows.value.length - 1)
  } else if (event.key === 'ArrowRight' && row.node.dir) {
    event.preventDefault()
    if (collapsed.value.has(row.node.path)) toggleDir(row.node.path)
    else if (rows.value[index + 1]?.depth === row.depth + 1) focusTreeRow(index + 1)
  } else if (event.key === 'ArrowLeft') {
    event.preventDefault()
    if (row.node.dir && !collapsed.value.has(row.node.path)) {
      toggleDir(row.node.path)
      return
    }
    for (let parentIndex = index - 1; parentIndex >= 0; parentIndex -= 1) {
      if (rows.value[parentIndex].depth < row.depth) {
        focusTreeRow(parentIndex)
        return
      }
    }
  } else if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    selectRow(row)
  }
}

function isCurrentProject(projectName: string, ctx: FarosContext | null): boolean {
  return props.projectName === projectName && props.ctx === ctx
}

async function loadTree() {
  const projectName = props.projectName
  const requestContext = props.ctx
  if (!projectName) {
    treeRequestSerial++
    loadingTree.value = false
    treeError.value = null
    return
  }
  const serial = ++treeRequestSerial
  loadingTree.value = true
  treeError.value = null
  try {
    const list = await api.listProjectFiles(requestContext, projectName)
    if (serial !== treeRequestSerial || !isCurrentProject(projectName, requestContext)) return
    const nextFiles = list.files ?? []
    files.value = nextFiles
    if (selectedPath.value && !nextFiles.some((file) => file.path === selectedPath.value)) {
      fileRequestSerial++
      selectedPath.value = ''
      content.value = null
      fileError.value = null
      loadingFile.value = false
    }
    // Auto-open the first file for orientation.
    if (!selectedPath.value && files.value.length > 0) {
      void openFile(files.value.slice().sort((a, b) => a.path.localeCompare(b.path))[0].path, projectName, requestContext)
    }
  } catch (e) {
    if (serial !== treeRequestSerial || !isCurrentProject(projectName, requestContext)) return
    treeError.value = e instanceof Error ? e.message : 'Could not load the workspace files.'
  } finally {
    if (serial === treeRequestSerial && isCurrentProject(projectName, requestContext)) loadingTree.value = false
  }
}

async function openFile(path: string, projectName = props.projectName, requestContext = props.ctx) {
  if (!projectName) return
  const serial = ++fileRequestSerial
  selectedPath.value = path
  loadingFile.value = true
  fileError.value = null
  content.value = null
  try {
    const nextContent = await api.readProjectFile(requestContext, projectName, path)
    if (serial !== fileRequestSerial || !isCurrentProject(projectName, requestContext) || selectedPath.value !== path) return
    content.value = nextContent
  } catch (e) {
    if (serial !== fileRequestSerial || !isCurrentProject(projectName, requestContext) || selectedPath.value !== path) return
    fileError.value = e instanceof Error ? e.message : 'Could not read this file.'
  } finally {
    if (serial === fileRequestSerial && isCurrentProject(projectName, requestContext) && selectedPath.value === path) loadingFile.value = false
  }
}

async function refreshWorkspaceSnapshot() {
  const projectName = props.projectName
  const requestContext = props.ctx
  if (!projectName) return
  const serial = ++workspaceRefreshSerial
  const path = selectedPath.value
  await loadTree()
  if (serial !== workspaceRefreshSerial || !isCurrentProject(projectName, requestContext)) return
  if (path && selectedPath.value === path && files.value.some((file) => file.path === path)) {
    await openFile(path, projectName, requestContext)
  }
}

/** Reload the tree after a write and select the written file. */
async function refreshAndSelect(path: string, projectName: string, requestContext: FarosContext | null) {
  workspaceRefreshSerial++
  await loadTree()
  if (!isCurrentProject(projectName, requestContext) || !files.value.some((file) => file.path === path)) return
  const parent = projectFileParentDir(path)
  if (parent) {
    const next = new Set(collapsed.value)
    let dir = parent
    while (dir) {
      next.delete(dir)
      dir = projectFileParentDir(dir)
    }
    collapsed.value = next
  }
  activeTreePath.value = path
  await openFile(path, projectName, requestContext)
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

// ── Upload ────────────────────────────────────────────────────────────────

/** Drop oversized files with a friendly message; the rest may upload. */
function acceptUploadCandidates(candidates: File[]): File[] {
  const accepted: File[] = []
  const rejected: string[] = []
  for (const file of candidates) {
    const sizeError = projectFileSizeError(file)
    if (sizeError) rejected.push(sizeError)
    else accepted.push(file)
  }
  if (rejected.length === 1) toast('error', rejected[0])
  else if (rejected.length > 1) toast('error', `${rejected.length} files are larger than 25 MiB and were skipped.`)
  return accepted
}

function openUploadPicker() {
  if (writesDisabled.value) return
  const input = uploadInputRef.value
  if (!input) return
  input.value = ''
  input.click()
}

function handleUploadInput(event: Event) {
  const input = event.target instanceof HTMLInputElement ? event.target : null
  const picked = input?.files ? Array.from(input.files) : []
  if (input) input.value = ''
  const accepted = acceptUploadCandidates(picked)
  if (!accepted.length) return
  newFileOpen.value = false
  uploadDraftError.value = ''
  uploadDraft.value = { files: accepted, dir: contextDir.value }
  void nextTick(() => uploadDirInputRef.value?.focus())
}

function cancelUploadDraft() {
  uploadDraft.value = null
  uploadDraftError.value = ''
}

function submitUploadDraft() {
  const draft = uploadDraft.value
  if (!draft) return
  const dir = normalizeProjectFileDir(draft.dir)
  if (dir === null) {
    uploadDraftError.value = 'Use a folder inside the project, such as public/assets.'
    return
  }
  uploadDraft.value = null
  uploadDraftError.value = ''
  void uploadFiles(draft.files, dir)
}

const uploadDraftBytes = computed(() => uploadDraft.value?.files.reduce((total, file) => total + file.size, 0) ?? 0)

async function uploadFiles(candidates: File[], dir: string) {
  const projectName = props.projectName
  const requestContext = props.ctx
  if (!projectName) return
  if (props.assistantBusy) {
    toast('info', 'Wait for the assistant run to finish, then upload again.')
    return
  }
  if (writeBusy.value) return
  const pending = acceptUploadCandidates(candidates)
  if (!pending.length) return
  writeBusy.value = true
  const uploaded: ProjectFileWriteResult[] = []
  const failures: string[] = []
  let skipped = 0
  try {
    for (const [index, file] of pending.entries()) {
      if (!isCurrentProject(projectName, requestContext)) return
      writeStatus.value = pending.length === 1
        ? `Uploading ${file.name}…`
        : `Uploading ${index + 1} of ${pending.length}…`
      const target = joinProjectFilePath(dir, file.name)
      try {
        uploaded.push(...await api.uploadProjectFiles(requestContext, projectName, [file], { dir }))
      } catch (error) {
        if (isProjectFileRequestError(error) && error.reason === 'exists') {
          const replace = await confirmDialog({
            title: `Replace ${file.name}?`,
            message: `${target} already exists. Uploading replaces it.`,
            confirmLabel: 'Replace',
            danger: true,
          })
          if (!replace) {
            skipped += 1
            continue
          }
          if (!isCurrentProject(projectName, requestContext)) return
          try {
            uploaded.push(...await api.uploadProjectFiles(requestContext, projectName, [file], { dir, overwrite: true }))
          } catch (retryError) {
            failures.push(errorMessage(retryError, `Could not upload ${file.name}.`))
            if (isProjectFileRequestError(retryError) && retryError.reason === 'busy') break
          }
          continue
        }
        failures.push(isProjectFileRequestError(error) && error.reason !== 'other'
          ? `${file.name}: ${error.message}`
          : errorMessage(error, `Could not upload ${file.name}.`))
        // The reservation covers the whole project; later files would fail too.
        if (isProjectFileRequestError(error) && error.reason === 'busy') break
      }
    }
  } finally {
    if (isCurrentProject(projectName, requestContext)) {
      writeBusy.value = false
      writeStatus.value = ''
    }
  }
  if (!isCurrentProject(projectName, requestContext)) return
  if (uploaded.length === 1) toast('ok', `Uploaded ${uploaded[0].path}.`)
  else if (uploaded.length > 1) toast('ok', `Uploaded ${uploaded.length} files to ${dirLabel(dir)}.`)
  if (failures.length === 1) toast('error', failures[0])
  else if (failures.length > 1) toast('error', `${failures.length} uploads failed. ${failures[0]}`)
  if (skipped && !uploaded.length && !failures.length) toast('info', skipped === 1 ? 'Upload cancelled.' : `${skipped} uploads cancelled.`)
  if (uploaded.length) {
    await refreshAndSelect(uploaded[uploaded.length - 1].path, projectName, requestContext)
  }
}

// ── Drag and drop onto the tree ───────────────────────────────────────────

function dropDirFromEvent(event: DragEvent): string {
  const target = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-drop-dir]') : null
  return target?.dataset.dropDir ?? ''
}

function handleTreeDragEnter(event: DragEvent) {
  if (!dragCarriesFiles(event)) return
  event.preventDefault()
  dragDepth += 1
  dropTargetDir.value = writesDisabled.value ? null : dropDirFromEvent(event)
}

function handleTreeDragOver(event: DragEvent) {
  if (!dragCarriesFiles(event)) return
  // Always claim the drop so the browser never navigates to the file.
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = writesDisabled.value ? 'none' : 'copy'
  dropTargetDir.value = writesDisabled.value ? null : dropDirFromEvent(event)
}

function handleTreeDragLeave(event: DragEvent) {
  if (!dragCarriesFiles(event)) return
  dragDepth = Math.max(0, dragDepth - 1)
  if (dragDepth === 0) dropTargetDir.value = null
}

function handleTreeDrop(event: DragEvent) {
  if (!dragCarriesFiles(event)) return
  event.preventDefault()
  const dir = dropDirFromEvent(event)
  dragDepth = 0
  dropTargetDir.value = null
  const { files: dropped, skippedFolders } = droppedFiles(event.dataTransfer)
  if (skippedFolders) toast('info', skippedFolders === 1 ? 'Folders cannot be uploaded. Drop the files inside it instead.' : `${skippedFolders} folders were skipped. Drop the files inside them instead.`)
  if (!dropped.length) return
  if (props.assistantBusy) {
    toast('info', 'Wait for the assistant run to finish, then upload again.')
    return
  }
  if (writeBusy.value) {
    toast('info', 'Another file change is still in progress.')
    return
  }
  void uploadFiles(dropped, dir)
}

// ── New file ──────────────────────────────────────────────────────────────

function openNewFile() {
  if (writesDisabled.value) return
  uploadDraft.value = null
  newFileError.value = ''
  newFilePath.value = contextDir.value ? `${contextDir.value}/` : ''
  newFileOpen.value = true
  void nextTick(() => {
    const input = newFileInputRef.value
    if (!input) return
    input.focus()
    input.setSelectionRange(input.value.length, input.value.length)
  })
}

function cancelNewFile() {
  newFileOpen.value = false
  newFileError.value = ''
}

async function createFile() {
  const projectName = props.projectName
  const requestContext = props.ctx
  const path = normalizeProjectFilePath(newFilePath.value)
  if (!path) {
    newFileError.value = 'Enter a file path inside the project, such as src/notes.md.'
    return
  }
  if (files.value.some((file) => file.path === path)) {
    newFileError.value = 'A file already exists at that path.'
    return
  }
  if (directoryPaths.value.has(path)) {
    newFileError.value = 'A folder already uses that path.'
    return
  }
  if (writesDisabled.value) return
  writeBusy.value = true
  writeStatus.value = `Creating ${projectFileBaseName(path)}…`
  newFileError.value = ''
  try {
    await api.putProjectFile(requestContext, projectName, path, '', { createOnly: true })
    if (!isCurrentProject(projectName, requestContext)) return
    newFileOpen.value = false
    toast('ok', `Created ${path}.`)
  } catch (error) {
    if (!isCurrentProject(projectName, requestContext)) return
    newFileError.value = errorMessage(error, 'Could not create the file.')
    return
  } finally {
    if (isCurrentProject(projectName, requestContext)) {
      writeBusy.value = false
      writeStatus.value = ''
    }
  }
  await refreshAndSelect(path, projectName, requestContext)
}

// ── Per-file actions ──────────────────────────────────────────────────────

async function downloadSelected() {
  const projectName = props.projectName
  const requestContext = props.ctx
  const path = selectedPath.value
  if (!projectName || !path || downloadBusy.value) return
  downloadBusy.value = true
  try {
    // files/raw needs the provider auth headers, so fetch the bytes and hand
    // the browser an object URL instead of linking to the route.
    const blob = await api.fetchProjectFileRaw(requestContext, projectName, path, { download: true })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = projectFileBaseName(path)
    link.hidden = true
    ;(rootRef.value ?? document.body).appendChild(link)
    link.click()
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
  } catch (error) {
    toast('error', errorMessage(error, `Could not download ${projectFileBaseName(path)}.`))
  } finally {
    downloadBusy.value = false
  }
}

async function deleteSelected() {
  const projectName = props.projectName
  const requestContext = props.ctx
  const path = selectedPath.value
  if (!projectName || !path || writesDisabled.value) return
  const confirmed = await confirmDialog({
    title: `Delete ${projectFileBaseName(path)}?`,
    message: `${path} will be removed from the workspace.`,
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!confirmed || !isCurrentProject(projectName, requestContext) || writesDisabled.value) return
  const version = content.value?.path === path ? content.value.version : undefined
  writeBusy.value = true
  writeStatus.value = `Deleting ${projectFileBaseName(path)}…`
  try {
    await api.deleteProjectFile(requestContext, projectName, path, version ? { ifMatch: version } : {})
    if (!isCurrentProject(projectName, requestContext)) return
    toast('ok', `Deleted ${path}.`)
  } catch (error) {
    if (!isCurrentProject(projectName, requestContext)) return
    toast('error', errorMessage(error, `Could not delete ${path}.`))
    return
  } finally {
    if (isCurrentProject(projectName, requestContext)) {
      writeBusy.value = false
      writeStatus.value = ''
    }
  }
  await refreshWorkspaceSnapshot()
}

// ── Viewer ────────────────────────────────────────────────────────────────

const contentLines = computed(() => (content.value?.content ?? '').split('\n'))
const treeState = computed(() => codeExplorerTreeState(loadingTree.value, files.value.length > 0, treeError.value))
const selectedIsImage = computed(() => !!content.value && projectFileHasImagePreview(content.value.path))
const showFilePanel = computed(() => !!content.value && (content.value.binary || (selectedIsImage.value && !showImageSource.value)))
const selectedSizeLabel = computed(() => formatByteSize(content.value?.size))

const IMAGE_TYPES: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
}

function releasePreview() {
  previewController?.abort()
  previewController = null
  if (preview.value.url) URL.revokeObjectURL(preview.value.url)
  preview.value = { status: 'idle', url: '', path: '' }
}

async function loadPreview(file: ProjectFileContent) {
  const projectName = props.projectName
  const requestContext = props.ctx
  releasePreview()
  const controller = new AbortController()
  previewController = controller
  preview.value = { status: 'loading', url: '', path: file.path }
  try {
    const blob = await api.fetchProjectFileRaw(requestContext, projectName, file.path, { signal: controller.signal })
    if (previewController !== controller || !isCurrentProject(projectName, requestContext)) return
    // <img> needs an image type; the server's type is by extension but may
    // fall back to octet-stream.
    const extension = file.path.split('.').pop()?.toLowerCase() ?? ''
    const typed = blob.type.startsWith('image/') ? blob : new Blob([blob], { type: IMAGE_TYPES[extension] ?? 'application/octet-stream' })
    previewController = null
    preview.value = { status: 'ready', url: URL.createObjectURL(typed), path: file.path }
  } catch {
    if (previewController !== controller || controller.signal.aborted) return
    previewController = null
    preview.value = { status: 'error', url: '', path: file.path }
  }
}

watch(content, (next) => {
  showImageSource.value = false
  if (next && projectFileHasImagePreview(next.path)) void loadPreview(next)
  else releasePreview()
})

watch(
  () => [props.projectName, props.ctx] as const,
  () => {
    workspaceRefreshSerial++
    treeRequestSerial++
    fileRequestSerial++
    files.value = []
    selectedPath.value = ''
    content.value = null
    loadingTree.value = false
    loadingFile.value = false
    treeError.value = null
    fileError.value = null
    collapsed.value = new Set()
    mobileTreeOpen.value = true
    activeTreePath.value = ''
    writeBusy.value = false
    writeStatus.value = ''
    uploadDraft.value = null
    newFileOpen.value = false
    releasePreview()
    if (props.projectName) void loadTree()
  },
  { immediate: true },
)

watch(rows, (visibleRows) => {
  if (visibleRows.length === 0) {
    activeTreePath.value = ''
    return
  }
  if (!visibleRows.some((row) => row.node.path === activeTreePath.value)) {
    activeTreePath.value = visibleRows.find((row) => row.node.path === selectedPath.value)?.node.path ?? visibleRows[0].node.path
  }
})

watch(
  () => props.refreshRevision,
  () => {
    if (props.projectName) void refreshWorkspaceSnapshot()
  },
)

watch(() => props.assistantBusy, (busy) => {
  if (busy) {
    uploadDraft.value = null
    newFileOpen.value = false
  }
})

onBeforeUnmount(releasePreview)
</script>

<template>
  <div ref="rootRef" class="flex h-full min-h-0 flex-col md:flex-row">
    <!-- Tree -->
    <aside
      class="relative w-full min-h-0 flex-1 flex-col md:w-64 md:flex-none md:border-r md:border-border-subtle"
      :class="mobileTreeOpen ? 'flex' : 'hidden md:flex'"
      @dragenter="handleTreeDragEnter"
      @dragover="handleTreeDragOver"
      @dragleave="handleTreeDragLeave"
      @drop="handleTreeDrop"
    >
      <div class="flex items-center justify-between gap-1 border-b border-border-subtle px-3 py-2">
        <span class="shrink-0 whitespace-nowrap text-[12px] font-semibold text-text-secondary">Workspace files</span>
        <span v-if="treeState === 'refreshing' && !writeBusy" class="mr-auto min-w-0 truncate text-[11px] text-text-muted" role="status" aria-live="polite">Refreshing…</span>
        <span v-else class="mr-auto" />
        <button
          type="button"
          class="app-studio-touch-target flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
          :title="`Upload files to ${dirLabel(contextDir)}`"
          aria-label="Upload files"
          :disabled="writesDisabled"
          @click="openUploadPicker"
        >
          <Upload class="h-4 w-4" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="app-studio-touch-target flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
          title="New file"
          aria-label="New file"
          :disabled="writesDisabled"
          @click="openNewFile"
        >
          <FilePlus class="h-4 w-4" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="app-studio-touch-target flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:opacity-50"
          title="Refresh"
          aria-label="Refresh workspace files"
          :disabled="loadingTree"
          @click="refreshWorkspaceSnapshot"
        >
          <Loader2 v-if="loadingTree" class="h-4 w-4 animate-spin" aria-hidden="true" />
          <RefreshCw v-else class="h-4 w-4" aria-hidden="true" />
        </button>
        <input ref="uploadInputRef" type="file" class="hidden" multiple @change="handleUploadInput" />
      </div>

      <p v-if="assistantBusy && projectName" class="border-b border-border-subtle px-3 py-1.5 text-[11px] text-text-muted" role="status">
        File changes are paused while the assistant runs.
      </p>
      <div v-else-if="writeBusy" class="flex items-center gap-2 border-b border-border-subtle px-3 py-1.5 text-[11px] text-text-secondary" role="status" aria-live="polite">
        <Loader2 class="h-3.5 w-3.5 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        <span class="truncate">{{ writeStatus || 'Saving…' }}</span>
      </div>

      <form
        v-if="uploadDraft"
        class="grid gap-2 border-b border-border-subtle px-3 py-2.5"
        aria-label="Upload destination"
        @submit.prevent="submitUploadDraft"
        @keydown.esc.prevent="cancelUploadDraft"
      >
        <p class="text-[12px] text-text-secondary">
          {{ uploadDraft.files.length === 1 ? uploadDraft.files[0].name : `${uploadDraft.files.length} files` }}
          <span class="text-text-muted">· {{ formatByteSize(uploadDraftBytes) }}</span>
        </p>
        <label class="grid gap-1 text-[11px] text-text-muted">
          Folder
          <input
            ref="uploadDirInputRef"
            v-model="uploadDraft.dir"
            class="k-input h-8 min-w-0 font-mono text-[16px] md:text-[12px]"
            placeholder="Project root"
            spellcheck="false"
            autocomplete="off"
          />
        </label>
        <p v-if="uploadDraftError" class="text-[11px] text-danger" role="alert">{{ uploadDraftError }}</p>
        <div class="flex items-center gap-2">
          <button type="submit" class="k-btn k-btn--primary h-8">Upload</button>
          <button type="button" class="k-btn k-btn--text h-8" @click="cancelUploadDraft">Cancel</button>
        </div>
      </form>

      <form
        v-if="newFileOpen"
        class="grid gap-2 border-b border-border-subtle px-3 py-2.5"
        aria-label="New file"
        @submit.prevent="createFile"
        @keydown.esc.prevent="cancelNewFile"
      >
        <label class="grid gap-1 text-[11px] text-text-muted">
          File path
          <input
            ref="newFileInputRef"
            v-model="newFilePath"
            class="k-input h-8 min-w-0 font-mono text-[16px] md:text-[12px]"
            placeholder="src/notes.md"
            spellcheck="false"
            autocomplete="off"
            :aria-invalid="newFileError ? 'true' : undefined"
          />
        </label>
        <p v-if="newFileError" class="text-[11px] text-danger" role="alert">{{ newFileError }}</p>
        <div class="flex items-center gap-2">
          <button type="submit" class="k-btn k-btn--primary h-8" :disabled="writesDisabled">Create</button>
          <button type="button" class="k-btn k-btn--text h-8" @click="cancelNewFile">Cancel</button>
        </div>
      </form>

      <div
        class="min-h-0 flex-1 overflow-auto py-1"
        :class="dropTargetDir === '' ? 'bg-accent-subtle ring-1 ring-inset ring-accent/50' : ''"
        data-drop-dir=""
      >
        <p v-if="treeState === 'initial-error'" class="px-3 py-2 text-[12px] text-danger" role="alert">{{ treeError }}</p>
        <div v-else-if="treeState === 'refresh-error'" class="mx-2 mb-2 grid gap-1 rounded-md border border-warning/30 bg-warning-subtle px-2.5 py-2 text-[11px] leading-4 text-warning" role="alert" aria-live="polite">
          <span>{{ treeError }}</span>
          <span class="text-text-muted">Showing the last loaded tree.</span>
          <button type="button" class="app-studio-touch-target inline-flex w-fit items-center rounded-sm font-medium underline underline-offset-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40" @click="refreshWorkspaceSnapshot">Retry refresh</button>
        </div>
        <div v-else-if="treeState === 'initial-loading'" class="grid gap-2 px-3 py-3" role="status" aria-live="polite" aria-label="Loading workspace files">
          <span class="sr-only">Loading workspace files…</span>
          <div v-for="width in ['w-4/5', 'w-3/5', 'w-2/3', 'w-1/2', 'w-3/4']" :key="width" class="shimmer h-4 rounded bg-surface-overlay" :class="width" />
        </div>
        <p v-else-if="!projectName" class="px-3 py-2 text-[12px] text-text-muted" role="status">
          Select a project to browse its workspace files.
        </p>
        <p v-else-if="treeState === 'empty'" class="px-3 py-2 text-[12px] text-text-muted" role="status">
          No files yet. Upload files, or bind a template with starter code.
        </p>
        <div v-if="rows.length" ref="treeRootRef" role="tree" aria-label="Workspace files">
          <button
            v-for="(row, index) in rows"
            :key="row.node.path"
            type="button"
            role="treeitem"
            :aria-level="row.depth + 1"
            :aria-posinset="row.posInSet"
            :aria-setsize="row.setSize"
            :aria-expanded="row.node.dir ? !collapsed.has(row.node.path) : undefined"
            :aria-selected="!row.node.dir && row.node.path === selectedPath"
            :tabindex="activeTreePath === row.node.path ? 0 : -1"
            :data-drop-dir="row.node.dir ? row.node.path : projectFileParentDir(row.node.path)"
            class="app-studio-touch-target flex w-full items-center gap-1.5 py-1 pr-2 text-left text-[13px] transition hover:bg-surface-hover"
            :class="[
              !row.node.dir && row.node.path === selectedPath ? 'bg-accent/10 text-accent' : 'text-text-secondary',
              row.node.dir && dropTargetDir === row.node.path ? 'bg-accent-subtle text-accent ring-1 ring-inset ring-accent/50' : '',
            ]"
            :style="{ paddingLeft: `${8 + row.depth * 14}px` }"
            @focus="activeTreePath = row.node.path"
            @keydown="handleTreeKeydown($event, row, index)"
            @click="selectRow(row)"
          >
            <FolderOpen v-if="row.node.dir && !collapsed.has(row.node.path)" class="h-3.5 w-3.5 shrink-0 text-text-muted" aria-hidden="true" />
            <Folder v-else-if="row.node.dir" class="h-3.5 w-3.5 shrink-0 text-text-muted" aria-hidden="true" />
            <FileIcon v-else class="h-3.5 w-3.5 shrink-0 text-text-muted" aria-hidden="true" />
            <span class="truncate">{{ row.node.name }}</span>
          </button>
        </div>
      </div>

      <p
        v-if="dropTargetDir !== null"
        class="pointer-events-none border-t border-accent/40 bg-accent-subtle px-3 py-2 text-[12px] text-accent"
        aria-hidden="true"
      >
        Drop to upload to {{ dirLabel(dropTargetDir) }}
      </p>
    </aside>

    <!-- Viewer -->
    <section class="min-w-0 flex-1 flex-col" :class="mobileTreeOpen ? 'hidden md:flex' : 'flex'">
      <div class="flex items-center gap-2 border-b border-border-subtle px-3 py-2">
        <button
          ref="mobileBackRef"
          type="button"
          class="app-studio-touch-target -ml-2 flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-secondary hover:bg-surface-hover hover:text-text-primary md:hidden"
          aria-label="Back to workspace files"
          @click="showMobileTree"
        >
          <ChevronLeft class="h-4 w-4" aria-hidden="true" />
        </button>
        <FileIcon class="h-3.5 w-3.5 shrink-0 text-text-muted" aria-hidden="true" />
        <span class="truncate text-[12px] font-medium text-text-primary">{{ selectedPath || 'Select a file' }}</span>
        <div class="ml-auto flex shrink-0 items-center gap-2">
          <span v-if="content?.truncated" class="rounded bg-surface-overlay px-1.5 py-0.5 text-[11px] text-text-muted">truncated</span>
          <button
            v-if="selectedIsImage && content && !content.binary"
            type="button"
            class="app-studio-touch-target rounded-sm px-1.5 py-0.5 text-[11px] font-medium text-text-secondary hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            @click="showImageSource = !showImageSource"
          >
            {{ showImageSource ? 'View image' : 'View source' }}
          </button>
          <template v-if="selectedPath">
            <button
              type="button"
              class="app-studio-touch-target flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:opacity-50"
              title="Download"
              :aria-label="`Download ${projectFileBaseName(selectedPath)}`"
              :disabled="downloadBusy"
              @click="downloadSelected"
            >
              <Loader2 v-if="downloadBusy" class="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              <Download v-else class="h-4 w-4" aria-hidden="true" />
            </button>
            <button
              type="button"
              class="app-studio-touch-target flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-danger-subtle hover:text-danger disabled:cursor-not-allowed disabled:opacity-50"
              title="Delete"
              :aria-label="`Delete ${projectFileBaseName(selectedPath)}`"
              :disabled="writesDisabled"
              @click="deleteSelected"
            >
              <Trash2 class="h-4 w-4" aria-hidden="true" />
            </button>
          </template>
        </div>
      </div>
      <div class="min-h-0 flex-1 overflow-auto">
        <div v-if="loadingFile" class="flex items-center gap-2 px-4 py-3 text-[13px] text-text-muted" role="status" aria-live="polite">
          <Loader2 class="h-4 w-4 animate-spin" /> Loading…
        </div>
        <p v-else-if="fileError" class="px-4 py-3 text-[13px] text-danger" role="alert">{{ fileError }}</p>
        <p v-else-if="!selectedPath" class="px-4 py-3 text-[13px] text-text-muted">Pick a file from the tree to view it.</p>
        <div v-else-if="content && showFilePanel" class="grid gap-3 px-4 py-4">
          <div class="flex flex-wrap items-center gap-3">
            <ImageIcon v-if="selectedIsImage" class="h-6 w-6 shrink-0 text-text-muted" :stroke-width="1.5" aria-hidden="true" />
            <FileIcon v-else class="h-6 w-6 shrink-0 text-text-muted" :stroke-width="1.5" aria-hidden="true" />
            <div class="min-w-0">
              <p class="truncate text-[13px] font-medium text-text-primary">{{ projectFileBaseName(content.path) }}</p>
              <p class="text-[12px] text-text-muted">{{ [selectedSizeLabel, content.binary ? 'Binary file' : 'Image'].filter(Boolean).join(' · ') }}</p>
            </div>
            <button
              type="button"
              class="k-btn k-btn--ghost ml-auto h-8"
              :disabled="downloadBusy"
              :aria-busy="downloadBusy ? 'true' : undefined"
              @click="downloadSelected"
            >
              <Loader2 v-if="downloadBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              <Download v-else class="h-3.5 w-3.5" aria-hidden="true" />
              {{ downloadBusy ? 'Downloading…' : 'Download' }}
            </button>
          </div>
          <div
            v-if="selectedIsImage"
            class="grid min-h-40 place-items-center overflow-hidden rounded-md border border-border-subtle bg-surface-overlay p-3"
            :aria-busy="preview.status === 'loading' ? 'true' : undefined"
          >
            <img
              v-if="preview.status === 'ready' && preview.path === content.path"
              :src="preview.url"
              :alt="projectFileBaseName(content.path)"
              class="max-h-[60vh] max-w-full object-contain"
            />
            <span v-else-if="preview.status === 'error'" class="text-[12px] text-text-muted" role="status">Preview unavailable. Download the file to open it.</span>
            <span v-else class="flex items-center gap-2 text-[12px] text-text-muted" role="status">
              <Loader2 class="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden="true" /> Loading preview…
            </span>
          </div>
          <p v-else class="text-[12px] text-text-muted">No preview for this file type. Download it to open it locally.</p>
        </div>
        <pre v-else class="m-0 flex text-[12px] leading-5"><code class="block w-full">
<span
  v-for="(line, i) in contentLines"
  :key="i"
  class="flex"
><span class="select-none border-r border-border-subtle px-2 text-right text-text-muted" style="min-width: 3rem">{{ i + 1 }}</span><span class="whitespace-pre px-3 text-text-primary">{{ line }}</span></span></code></pre>
      </div>
    </section>
  </div>
</template>
