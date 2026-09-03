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
import { computed, nextTick, ref, watch } from 'vue'
import { ChevronLeft, RefreshCw, Folder, FolderOpen, File as FileIcon, Loader2 } from 'lucide-vue-next'
import type { FarosContext, ProjectFileInfo, ProjectFileContent } from './types'
import { api } from './api'

// A minimal read-only explorer of the live development workspace — the same
// files the assistant edits and development sync pushes into the sandbox.
// Left: a collapsible tree built from the flat path list. Right: the selected
// file's content. No editing; this is a window into what the dev env runs.

const props = defineProps<{
  ctx: FarosContext | null
  projectName: string
}>()

const files = ref<ProjectFileInfo[]>([])
const loadingTree = ref(false)
const treeError = ref<string | null>(null)
let treeRequestSerial = 0

const selectedPath = ref<string>('')
const content = ref<ProjectFileContent | null>(null)
const loadingFile = ref(false)
const fileError = ref<string | null>(null)
let fileRequestSerial = 0

const collapsed = ref<Set<string>>(new Set())
const mobileTreeOpen = ref(true)
const activeTreePath = ref('')
const treeRootRef = ref<HTMLElement | null>(null)
const mobileBackRef = ref<HTMLButtonElement | null>(null)

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

const contentLines = computed(() => (content.value?.content ?? '').split('\n'))
const treeState = computed(() => codeExplorerTreeState(loadingTree.value, files.value.length > 0, treeError.value))

watch(
  () => [props.projectName, props.ctx] as const,
  () => {
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
</script>

<template>
  <div class="flex h-full min-h-0 flex-col md:flex-row">
    <!-- Tree -->
    <aside
      class="w-full min-h-0 flex-1 flex-col md:w-64 md:flex-none md:border-r md:border-border-subtle"
      :class="mobileTreeOpen ? 'flex' : 'hidden md:flex'"
    >
      <div class="flex items-center justify-between gap-2 border-b border-border-subtle px-3 py-2">
        <span class="text-[12px] font-semibold text-text-secondary">Workspace files</span>
        <span v-if="treeState === 'refreshing'" class="mr-auto text-[11px] text-text-muted" role="status" aria-live="polite">Refreshing…</span>
        <button
          type="button"
          class="app-studio-touch-target flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:opacity-50"
          title="Refresh"
          aria-label="Refresh workspace files"
          :disabled="loadingTree"
          @click="loadTree"
        >
          <Loader2 v-if="loadingTree" class="h-4 w-4 animate-spin" aria-hidden="true" />
          <RefreshCw v-else class="h-4 w-4" aria-hidden="true" />
        </button>
      </div>
      <div class="min-h-0 flex-1 overflow-auto py-1">
        <p v-if="treeState === 'initial-error'" class="px-3 py-2 text-[12px] text-danger" role="alert">{{ treeError }}</p>
        <div v-else-if="treeState === 'refresh-error'" class="mx-2 mb-2 grid gap-1 rounded-md border border-warning/30 bg-warning-subtle px-2.5 py-2 text-[11px] leading-4 text-warning" role="alert" aria-live="polite">
          <span>{{ treeError }}</span>
          <span class="text-text-muted">Showing the last loaded tree.</span>
          <button type="button" class="app-studio-touch-target inline-flex w-fit items-center rounded-sm font-medium underline underline-offset-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40" @click="loadTree">Retry refresh</button>
        </div>
        <div v-else-if="treeState === 'initial-loading'" class="grid gap-2 px-3 py-3" role="status" aria-live="polite" aria-label="Loading workspace files">
          <span class="sr-only">Loading workspace files…</span>
          <div v-for="width in ['w-4/5', 'w-3/5', 'w-2/3', 'w-1/2', 'w-3/4']" :key="width" class="shimmer h-4 rounded bg-surface-overlay" :class="width" />
        </div>
        <p v-else-if="!projectName" class="px-3 py-2 text-[12px] text-text-muted" role="status">
          Select a project to browse its workspace files.
        </p>
        <p v-else-if="treeState === 'empty'" class="px-3 py-2 text-[12px] text-text-muted" role="status">
          No files yet. Starter code appears here once a template with scaffolding is bound.
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
            class="app-studio-touch-target flex w-full items-center gap-1.5 py-1 pr-2 text-left text-[13px] transition hover:bg-surface-hover"
            :class="!row.node.dir && row.node.path === selectedPath ? 'bg-accent/10 text-accent' : 'text-text-secondary'"
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
        <span v-if="content?.truncated" class="ml-auto rounded bg-surface-overlay px-1.5 py-0.5 text-[11px] text-text-muted">truncated</span>
      </div>
      <div class="min-h-0 flex-1 overflow-auto">
        <div v-if="loadingFile" class="flex items-center gap-2 px-4 py-3 text-[13px] text-text-muted" role="status" aria-live="polite">
          <Loader2 class="h-4 w-4 animate-spin" /> Loading…
        </div>
        <p v-else-if="fileError" class="px-4 py-3 text-[13px] text-danger" role="alert">{{ fileError }}</p>
        <p v-else-if="content?.binary" class="px-4 py-3 text-[13px] text-text-muted">Binary file — not shown.</p>
        <p v-else-if="!selectedPath" class="px-4 py-3 text-[13px] text-text-muted">Pick a file from the tree to view it.</p>
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
