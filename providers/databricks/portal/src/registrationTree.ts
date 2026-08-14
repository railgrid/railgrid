export type RegistrationTreeNodeKind = 'catalog' | 'schema' | 'table' | 'warehouse'

export interface RegistrationTreeNode {
  id: string
  kind: RegistrationTreeNodeKind
  label: string
  detail?: string
  parentId?: string
  depth: number
  disabled: boolean
  disabledReason?: string
  expanded: boolean
  childrenLoaded: boolean
  loading: boolean
  error?: string
  nextPageToken?: string
  childIds: string[]
  catalog?: string
  schema?: string
  table?: string
  registration?: { warehouseID?: string }
}

export interface RegistrationTreeState {
  roots: string[]
  nodes: Record<string, RegistrationTreeNode>
  selectedLeafIds: string[]
}

export interface TreePage {
  nodes: RegistrationTreeNode[]
  nextPageToken?: string
}

export type TreeCheckState = 'false' | 'mixed' | 'true'

export const REGISTRATION_LIMIT = 50
export const MAX_PARENT_CHECK_PAGES = 100

export function emptyRegistrationTree(): RegistrationTreeState {
  return { roots: [], nodes: {}, selectedLeafIds: [] }
}

export function isLeaf(node: RegistrationTreeNode): boolean {
  return node.kind === 'table' || node.kind === 'warehouse'
}

export function appendTreePage(state: RegistrationTreeState, parentId: string | undefined, page: TreePage): void {
  const ids = parentId ? state.nodes[parentId]?.childIds : state.roots
  if (!ids) return
  for (const node of page.nodes) {
    if (!state.nodes[node.id]) state.nodes[node.id] = node
    if (!ids.includes(node.id)) ids.push(node.id)
  }
  if (parentId) {
    const parent = state.nodes[parentId]
    parent.childrenLoaded = true
    parent.nextPageToken = page.nextPageToken
  }
}

export function visibleTreeIds(state: RegistrationTreeState): string[] {
  const visible: string[] = []
  const visit = (id: string) => {
    const node = state.nodes[id]
    if (!node) return
    visible.push(id)
    if (node.expanded) node.childIds.forEach(visit)
  }
  state.roots.forEach(visit)
  return visible
}

export function loadedSelectableLeaves(state: RegistrationTreeState, nodeId: string): string[] {
  const leaves: string[] = []
  const visit = (id: string) => {
    const node = state.nodes[id]
    if (!node || node.disabled) return
    if (isLeaf(node)) leaves.push(id)
    else node.childIds.forEach(visit)
  }
  visit(nodeId)
  return leaves
}

export function treeCheckState(state: RegistrationTreeState, nodeId: string): TreeCheckState {
  const leaves = loadedSelectableLeaves(state, nodeId)
  if (!leaves.length) return 'false'
  const selected = new Set(state.selectedLeafIds)
  const count = leaves.filter(id => selected.has(id)).length
  if (count === 0) return 'false'
  const incomplete = (id: string): boolean => {
    const node = state.nodes[id]
    if (!node || node.disabled || isLeaf(node)) return false
    return !node.childrenLoaded || !!node.nextPageToken || node.childIds.some(incomplete)
  }
  return count === leaves.length && !incomplete(nodeId) ? 'true' : 'mixed'
}

export function updateLeafSelection(state: RegistrationTreeState, leafId: string, checked: boolean, limit = REGISTRATION_LIMIT): string | null {
  const node = state.nodes[leafId]
  if (!node || !isLeaf(node) || node.disabled) return null
  const selected = new Set(state.selectedLeafIds)
  if (checked && !selected.has(leafId) && selected.size >= limit) return `Register at most ${limit} resources at a time.`
  if (checked) selected.add(leafId); else selected.delete(leafId)
  state.selectedLeafIds = [...selected]
  return null
}

export function clearTreeSelection(state: RegistrationTreeState): void {
  state.selectedLeafIds = []
}

export interface ExhaustResult {
  complete: boolean
  state?: RegistrationTreeState
  leafIds: string[]
  reason?: string
  pages: number
}

function cloneState(state: RegistrationTreeState): RegistrationTreeState {
  return {
    roots: [...state.roots],
    selectedLeafIds: [...state.selectedLeafIds],
    nodes: Object.fromEntries(Object.entries(state.nodes).map(([id, node]) => [id, { ...node, childIds: [...node.childIds] }])),
  }
}

/**
 * Resolve every page below a branch in a private snapshot. Nothing is exposed
 * to the caller until the subtree is complete and fits the remaining limit.
 */
export async function exhaustBranchSelection(
  state: RegistrationTreeState,
  branchId: string,
  loadPage: (parent: RegistrationTreeNode, pageToken?: string) => Promise<TreePage>,
  remaining: number,
  maxPages = MAX_PARENT_CHECK_PAGES,
): Promise<ExhaustResult> {
  const draft = cloneState(state)
  const alreadySelected = new Set(state.selectedLeafIds)
  let pages = 0
  try {
    const eligibleLeaves = (): string[] => loadedSelectableLeaves(draft, branchId)
    const ensureWithinLimit = (): void => {
      const additions = eligibleLeaves().filter(id => !alreadySelected.has(id))
      if (additions.length > remaining) {
        throw new Error(`This branch contains at least ${additions.length} unselected resources, exceeding the ${remaining} remaining selection slots.`)
      }
    }
    const visit = async (id: string): Promise<void> => {
      const node = draft.nodes[id]
      if (!node || node.disabled || isLeaf(node)) return
      let token = node.childrenLoaded ? node.nextPageToken : undefined
      if (!node.childrenLoaded || token) {
        const seen = new Set<string>()
        do {
          if (pages >= maxPages) throw new Error(`Selection stopped after ${maxPages} discovery pages; select a smaller branch.`)
          const tokenKey = token ?? '<first>'
          if (seen.has(tokenKey)) throw new Error('Discovery returned a repeated page token; nothing was selected.')
          seen.add(tokenKey)
          const page = await loadPage(node, token)
          pages += 1
          appendTreePage(draft, id, page)
          ensureWithinLimit()
          token = page.nextPageToken
        } while (token)
      }
      ensureWithinLimit()
      for (const childId of node.childIds) await visit(childId)
    }
    ensureWithinLimit()
    await visit(branchId)
    const leafIds = eligibleLeaves()
    const additions = leafIds.filter(id => !alreadySelected.has(id))
    draft.selectedLeafIds = [...alreadySelected, ...additions]
    return { complete: true, state: draft, leafIds, pages }
  } catch (cause) {
    return { complete: false, leafIds: [], pages, reason: cause instanceof Error ? cause.message : String(cause) }
  }
}
