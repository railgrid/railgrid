<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { FileText, Image, Loader2, Paperclip, Plus, RotateCcw, Upload, X } from 'lucide-vue-next'
import { api, isProjectAPINotFoundError } from './api'
import AssistantCommandPalette from './AssistantCommandPalette.vue'
import AssistantAttachmentPreview from './AssistantAttachmentPreview.vue'
import AssistantAttachmentTextPreview from './AssistantAttachmentTextPreview.vue'
import AssistantMessageAnnotations from './AssistantMessageAnnotations.vue'
import {
  assistantComposerPlainContent,
  assistantSlashToken,
  consumeAssistantSlashToken,
  MAX_ASSISTANT_COMPOSER_PARTS,
  type AssistantComposerPart,
  type AssistantComposerState,
} from './assistantCommandPalette'
import { assistantResourceSelectionKey } from './assistantResources'
import {
  ASSISTANT_ATTACHMENT_ACCEPT,
  ASSISTANT_LARGE_PASTE_BYTES,
  assistantAttachmentIsImage,
  assistantAttachmentValidationError,
  assistantAttachmentIsSupported,
  assistantAttachmentPart,
  projectAssistantAttachmentReceipt,
  type AssistantAttachmentStatus,
} from './assistantAttachments'
import { useDismissibleAddMenu } from './useDismissibleAddMenu'
import { useAssistantFilePickerFocus } from './useAssistantFilePickerFocus'
import type {
  FarosContext,
  ProjectAssistantAttachmentReceipt,
  ProjectAssistantContentPart,
  ProjectAssistantContextResource,
  ProjectAssistantRunMode,
  ProjectAssistantSkill,
  ProviderItem,
} from './types'

const MAX_CHIPS = 8

const props = withDefaults(defineProps<{
  modelValue: string
  contentParts?: ProjectAssistantContentPart[]
  projectName: string
  skills: ProjectAssistantSkill[]
  selectedSkills?: ProjectAssistantSkill[]
  selectedResources?: ProjectAssistantContextResource[]
  ctx: FarosContext | null
  providers: ProviderItem[]
  disabled?: boolean
  activeRun?: boolean
  queueingEnabled?: boolean
  placeholder?: string
  annotationDocumentId?: string
  annotationPagePath?: string
  unresolvedAnnotationIds?: string[]
}>(), {
  contentParts: () => [],
  selectedSkills: () => [],
  selectedResources: () => [],
  disabled: false,
  activeRun: false,
  queueingEnabled: true,
  placeholder: 'Message this project',
  annotationDocumentId: '',
  annotationPagePath: '',
  unresolvedAnnotationIds: () => [],
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:contentParts': [value: ProjectAssistantContentPart[]]
  'update:selectedSkills': [value: ProjectAssistantSkill[]]
  'update:selectedResources': [value: ProjectAssistantContextResource[]]
  'update:attachmentsPending': [value: boolean]
  state: [value: AssistantComposerState]
  submit: [value: AssistantComposerState, intent: 'queue' | 'steer']
  selectMode: [mode: ProjectAssistantRunMode]
}>()

const rootRef = ref<HTMLDivElement | null>(null)
const addMenuRootRef = ref<HTMLDivElement | null>(null)
const attachmentMenuTriggerRef = ref<HTMLButtonElement | null>(null)
const editorRef = ref<HTMLDivElement | null>(null)
const commandPaletteOpen = ref(false)
const commandPaletteFromSlash = ref(false)
const commandPaletteQuery = ref('')
const attachmentMenuOpen = ref(false)
const attachmentInputRef = ref<HTMLInputElement | null>(null)
const composing = ref(false)
const suppressNextInput = ref(false)
const lastRenderedSignature = ref('')
const savedSelection = ref<Range | null>(null)
const slashTokenRef = ref<ReturnType<typeof assistantSlashToken>>(null)
const localParts = ref<ProjectAssistantContentPart[]>([])
const localSkills = ref<ProjectAssistantSkill[]>([])
const localResources = ref<ProjectAssistantContextResource[]>([])

interface AssistantAttachmentChip {
  clientID: string
  file?: File
  receipt?: ProjectAssistantAttachmentReceipt
  /** Project that accepted the upload; cleanup must not follow a later route. */
  projectName?: string
  /** The accepted turn now owns this receipt; the composer must not delete it. */
  committed?: boolean
  status: AssistantAttachmentStatus
  error?: string
  retryAction?: 'upload' | 'delete'
  controller?: AbortController
}

const attachmentChips = ref<AssistantAttachmentChip[]>([])

const { waitForPicker, restorePickerFocus } = useAssistantFilePickerFocus(() => {
  const editor = editorRef.value
  if (editor && editor.contentEditable !== 'false') return editor
  return attachmentMenuTriggerRef.value
})

const localAnnotations = computed(() => localParts.value
  .filter((part): part is Extract<ProjectAssistantContentPart, { type: 'annotation' }> => part.type === 'annotation')
  .map((part) => part.annotation))

const attachmentChipsPending = computed(() => attachmentChips.value.some((chip) => chip.status !== 'ready'))

function attachmentLabel(chip: AssistantAttachmentChip): string {
  return chip.receipt?.filename || chip.file?.name || 'attachment'
}

function attachmentCleanupIDs(chip: Pick<AssistantAttachmentChip, 'clientID' | 'receipt'>): string[] {
  return [...new Set([chip.receipt?.id, chip.clientID].filter((id): id is string => Boolean(id?.trim())))]
}

/** Best-effort cleanup for cancelled or ambiguously failed draft uploads. */
async function bestEffortDeleteAttachment(
  chip: Pick<AssistantAttachmentChip, 'clientID' | 'receipt'>,
  projectName: string,
): Promise<void> {
  if (!projectName) return
  for (const attachmentID of attachmentCleanupIDs(chip)) {
    try {
      await api.deleteAssistantAttachment(props.ctx, projectName, attachmentID)
    } catch {
      // A 409 is expected when the receipt was bound by an accepted turn:
      // bound attachment bytes are immutable. A 404 is expected when a draft
      // already expired or was removed. Network failures are also swallowed;
      // lifecycle cleanup must never replace a useful retry with an ambiguous
      // error state.
    }
  }
}

/**
 * Release browser-owned candidates without treating immutable receipts as a
 * lifecycle failure. The project captured on each chip is authoritative when
 * a route switch has already changed props.projectName.
 */
function cleanupAttachmentChips(chips: readonly AssistantAttachmentChip[], fallbackProjectName: string) {
  for (const chip of chips) {
    chip.controller?.abort()
    // App commits receipts at the POST acceptance boundary before clearing its
    // contentParts prop. They are no longer browser-owned, even if the server
    // has not finished binding them when a route/unmount cleanup runs.
    if (chip.committed) continue
    if (chip.status === 'deleting') continue
    void bestEffortDeleteAttachment(chip, chip.projectName || fallbackProjectName)
  }
}

function isAttachmentAbortError(error: unknown): boolean {
  return error instanceof DOMException
    ? error.name === 'AbortError'
    : error instanceof Error && error.name === 'AbortError'
}

function attachmentStatusLabel(chip: Pick<AssistantAttachmentChip, 'status' | 'retryAction'>): string {
  if (chip.status === 'staged') return 'Ready to attach'
  if (chip.status === 'uploading') return 'Uploading'
  if (chip.status === 'deleting') return 'Removing'
  if (chip.status === 'error') return chip.retryAction === 'delete' ? 'Removal failed' : chip.retryAction === 'upload' ? 'Upload failed' : 'Cannot attach'
  return 'Ready'
}

useDismissibleAddMenu({
  open: attachmentMenuOpen,
  root: addMenuRootRef,
  trigger: attachmentMenuTriggerRef,
  onClose: closeAttachmentMenu,
})

function emitAttachmentPending() {
  emit('update:attachmentsPending', attachmentChipsPending.value)
}

function reconcileAttachmentChips(parts: readonly ProjectAssistantContentPart[]) {
  const receipts = parts
    .filter((part): part is Extract<ProjectAssistantContentPart, { type: 'attachment' }> => part.type === 'attachment')
    .map((part) => part.attachment)
  const receiptIDs = new Set(receipts.map((receipt) => receipt.id))
  // `contentParts` can be cleared before the project watcher runs (for
  // example, when accepted-submit state is reset). A receipt handed to an
  // accepted turn is no longer browser-owned and must not be deleted while the
  // server finishes binding it. Unsubmitted ready drafts still get a
  // best-effort cleanup here before they leave the local chip set.
  for (const chip of attachmentChips.value) {
    if (chip.status === 'ready' && !chip.committed && chip.receipt && !receiptIDs.has(chip.receipt.id)) {
      void bestEffortDeleteAttachment(chip, chip.projectName || props.projectName)
    }
  }
  const retained = attachmentChips.value.filter((chip) => chip.status !== 'ready' || (chip.receipt && receiptIDs.has(chip.receipt.id)))
  const knownIDs = new Set(retained.flatMap((chip) => chip.receipt ? [chip.receipt.id] : []))
  for (const receipt of receipts) {
    if (knownIDs.has(receipt.id)) continue
    retained.push({ clientID: `receipt:${receipt.id}`, receipt, projectName: props.projectName, status: 'ready' })
    knownIDs.add(receipt.id)
  }
  attachmentChips.value = retained
  emitAttachmentPending()
}

const selectedSkillIDs = computed(() => localSkills.value.map((skill) => skill.id))

function partSignature(parts: readonly ProjectAssistantContentPart[]): string {
  return JSON.stringify(parts)
}

function stateSignature(content: string, parts: readonly ProjectAssistantContentPart[], skills: readonly ProjectAssistantSkill[], resources: readonly ProjectAssistantContextResource[]): string {
  return JSON.stringify({ content, parts, skills: skills.map((skill) => skill.id), resources: resources.map(assistantResourceSelectionKey) })
}

function chipLabel(part: ProjectAssistantContentPart): string {
  if (part.type === 'skill') return localSkills.value.find((skill) => skill.id === part.skillID)?.name || part.skillID
  if (part.type === 'resource') return localResources.value[part.resourceIndex]?.resourceRef.name || `resource ${part.resourceIndex + 1}`
  return ''
}

function chipKind(part: ProjectAssistantContentPart): 'skill' | 'resource' {
  if (part.type === 'resource') return 'resource'
  return 'skill'
}

function normalizedParts(): ProjectAssistantContentPart[] {
  const incoming = props.contentParts.filter((part) => part && typeof part === 'object')
  if (incoming.length) return incoming.slice(0, MAX_ASSISTANT_COMPOSER_PARTS)
  return props.modelValue ? [{ type: 'text', text: props.modelValue }] : []
}

function createChip(part: Exclude<ProjectAssistantContentPart, { type: 'text' | 'annotation' | 'attachment' }>): HTMLSpanElement {
  const chip = document.createElement('span')
  chip.dataset.assistantChip = chipKind(part)
  if (part.type === 'skill') chip.dataset.skillID = part.skillID
  if (part.type === 'resource') chip.dataset.resourceIndex = String(part.resourceIndex)
  chip.contentEditable = 'false'
  chip.className = 'assistant-composer-chip inline-flex max-w-full cursor-default select-none items-center gap-1 rounded-sm border border-accent/30 bg-accent/10 px-1.5 py-0.5 align-baseline text-[11px] leading-4 text-accent'
  chip.setAttribute('role', 'button')
  chip.setAttribute('tabindex', '0')
  chip.setAttribute('aria-label', `${chipKind(part)} ${chipLabel(part)}`)
  const icon = document.createElement('span')
  icon.className = 'assistant-composer-chip-icon'
  icon.textContent = chipKind(part) === 'skill' ? '@' : '#'
  const label = document.createElement('span')
  label.className = 'assistant-composer-chip-label max-w-48 truncate font-mono'
  label.textContent = chipLabel(part)
  chip.append(icon, label)
  return chip
}

function renderParts(
  parts: readonly ProjectAssistantContentPart[],
  content = props.modelValue,
  skills: readonly ProjectAssistantSkill[] = localSkills.value,
  resources: readonly ProjectAssistantContextResource[] = localResources.value,
) {
  const editor = editorRef.value
  if (!editor) return
  // Replacing children detaches every old Range. Do not let a later focus
  // restore a selection that points into the discarded DOM tree.
  savedSelection.value = null
  editor.replaceChildren()
  for (const part of parts) {
    if (part.type === 'text') editor.append(document.createTextNode(part.text))
    // Attachment receipts render in the adjacent receipt row; only editable
    // skill/resource parts become contenteditable chips. The old equivalent
    // was: if (part.type !== 'annotation') editor.append(createChip(part))
    else if (part.type !== 'annotation') {
      if (part.type !== 'attachment') editor.append(createChip(part))
    }
  }
  if (!editor.childNodes.length) editor.append(document.createTextNode(''))
  lastRenderedSignature.value = stateSignature(content, parts, skills, resources)
}

interface Segment {
  node: Node
  start: number
  end: number
  kind: 'text' | 'chip' | 'break'
}

function segmentText(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent || ''
  if (node instanceof HTMLElement && node.dataset.assistantChip) return node.textContent || ''
  if (node.nodeName === 'BR') return '\n'
  return node.textContent || ''
}

function collectSegments(): Segment[] {
  const root = editorRef.value
  if (!root) return []
  const segments: Segment[] = []
  let offset = 0
  const visit = (node: Node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      const text = node.textContent || ''
      if (text) segments.push({ node, start: offset, end: offset + text.length, kind: 'text' })
      offset += text.length
      return
    }
    if (node instanceof HTMLElement && node.dataset.assistantChip) {
      const text = segmentText(node)
      segments.push({ node, start: offset, end: offset + text.length, kind: 'chip' })
      offset += text.length
      return
    }
    if (node.nodeName === 'BR') {
      segments.push({ node, start: offset, end: offset + 1, kind: 'break' })
      offset += 1
      return
    }
    for (const child of Array.from(node.childNodes)) visit(child)
  }
  for (const child of Array.from(root.childNodes)) visit(child)
  return segments
}

function contentTextFromDOM(): string {
  const parts: string[] = []
  for (const segment of collectSegments()) {
    if (segment.kind === 'text' || segment.kind === 'break') parts.push(segmentText(segment.node))
  }
  return parts.join('')
}

/** Text used for caret offsets. Chips contribute their rendered label here so
 * a DOM offset remains stable while the model-visible content omits chips. */
function visibleTextFromDOM(): string {
  return collectSegments().map((segment) => segmentText(segment.node)).join('')
}

function partsFromDOM(): ProjectAssistantContentPart[] {
  const parts: ProjectAssistantContentPart[] = []
  const append = (part: ProjectAssistantContentPart) => {
    const previous = parts[parts.length - 1]
    if (part.type === 'text' && previous?.type === 'text') previous.text += part.text
    else if (parts.length < MAX_ASSISTANT_COMPOSER_PARTS && (part.type !== 'text' || part.text)) parts.push(part)
  }
  for (const segment of collectSegments()) {
    if (segment.kind === 'text') append({ type: 'text', text: segmentText(segment.node) })
    else if (segment.kind === 'break') append({ type: 'text', text: '\n' })
    else if (segment.node instanceof HTMLElement) {
      const kind = segment.node.dataset.assistantChip
      if (kind === 'skill' && segment.node.dataset.skillID) append({ type: 'skill', skillID: segment.node.dataset.skillID })
      if (kind === 'resource' && segment.node.dataset.resourceIndex) {
        const resourceIndex = Number(segment.node.dataset.resourceIndex)
        if (Number.isSafeInteger(resourceIndex) && resourceIndex >= 0) append({ type: 'resource', resourceIndex })
      }
    }
  }
  // Preview annotations and uploaded receipts are attachments, not editable
  // prose. Keep them out of the contenteditable DOM so caret movement and
  // chip deletion remain predictable, then append stable descriptors to the
  // submitted turn.
  for (const part of localParts.value) {
    if (part.type === 'annotation') append(part)
    else if (part.type === 'attachment') append(part)
  }
  return parts
}

function removeAllAnnotations() {
  if (!localAnnotations.value.length) return
  localParts.value = localParts.value.filter((part) => part.type !== 'annotation')
  emitState()
  focusEditor(false)
}

function emitState(): AssistantComposerState {
  let parts = partsFromDOM()
  const usedSkillIDs = new Set(parts.filter((part): part is Extract<ProjectAssistantContentPart, { type: 'skill' }> => part.type === 'skill').map((part) => part.skillID))
  localSkills.value = localSkills.value.filter((skill) => usedSkillIDs.has(skill.id))
  const usedResourceIndices = [...new Set(parts.filter((part): part is Extract<ProjectAssistantContentPart, { type: 'resource' }> => part.type === 'resource').map((part) => part.resourceIndex))].sort((left, right) => left - right)
  const resourceIndexMap = new Map<number, number>()
  const retainedResources: ProjectAssistantContextResource[] = []
  usedResourceIndices.forEach((index) => {
    const resource = localResources.value[index]
    if (!resource) return
    resourceIndexMap.set(index, retainedResources.length)
    retainedResources.push(resource)
  })
  localResources.value = retainedResources
  parts = parts.flatMap((part): ProjectAssistantContentPart[] => {
    if (part.type !== 'resource') return [part]
    const nextIndex = resourceIndexMap.get(part.resourceIndex)
    return nextIndex === undefined ? [] : [{ type: 'resource', resourceIndex: nextIndex }]
  })
  if (resourceIndexMap.size) {
    for (const chip of Array.from(editorRef.value?.querySelectorAll<HTMLElement>('[data-assistant-chip="resource"]') || [])) {
      const current = Number(chip.dataset.resourceIndex)
      const next = resourceIndexMap.get(current)
      if (next === undefined) chip.remove()
      else chip.dataset.resourceIndex = String(next)
    }
  }
  const content = assistantComposerPlainContent(parts as AssistantComposerPart[])
  localParts.value = parts
  // Input events mutate the contenteditable DOM before this callback runs.
  // Mark that DOM as rendered before notifying the parent: Vue queues the
  // resulting prop watcher, and a stale signature there would rebuild the
  // editor (detaching the native selection and making typing reverse).
  lastRenderedSignature.value = stateSignature(content, parts, localSkills.value, localResources.value)
  emit('update:modelValue', content)
  emit('update:contentParts', parts)
  emit('update:selectedSkills', [...localSkills.value])
  emit('update:selectedResources', [...localResources.value])
  const state: AssistantComposerState = {
    content,
    contentParts: parts as AssistantComposerPart[],
    skills: [...localSkills.value],
    contextResources: [...localResources.value],
    attachmentsPending: attachmentChipsPending.value,
  }
  emit('state', state)
  return state
}

function nodeOffset(node: Node, offset: number): number {
  const segments = collectSegments()
  const segment = segments.find((candidate) => candidate.node === node)
  if (segment) return segment.start + Math.max(0, Math.min(offset, segment.end - segment.start))
  if (node === editorRef.value) {
    const children = Array.from(node.childNodes)
    let total = 0
    for (let index = 0; index < offset; index++) total += segmentText(children[index] || document.createTextNode('')).length
    return total
  }
  return 0
}

function caretOffset(): number | null {
  const editor = editorRef.value
  const selection = window.getSelection()
  if (!editor || !selection || !selection.rangeCount || !selection.isCollapsed) return null
  const range = selection.getRangeAt(0)
  if (!editor.contains(range.startContainer)) return null
  return nodeOffset(range.startContainer, range.startOffset)
}

function selectionOffsets(): [number, number] | null {
  const editor = editorRef.value
  const selection = window.getSelection()
  if (!editor || !selection || !selection.rangeCount) return null
  const range = selection.getRangeAt(0)
  if (!editor.contains(range.startContainer) || !editor.contains(range.endContainer)) return null
  return [
    nodeOffset(range.startContainer, range.startOffset),
    nodeOffset(range.endContainer, range.endOffset),
  ]
}

function boundaryAt(offset: number, preferAfter = false): { node: Node; offset: number } | null {
  const editor = editorRef.value
  if (!editor) return null
  const segments = collectSegments()
  const bounded = Math.max(0, Math.min(offset, segments.length ? segments[segments.length - 1].end : 0))
  for (const segment of segments) {
    const parent = segment.node.parentNode || editor
    const siblings = Array.from(parent.childNodes) as Node[]
    const index = siblings.indexOf(segment.node)
    if (segment.kind === 'chip') {
      if (bounded === segment.start && !preferAfter) return { node: parent, offset: index }
      if (bounded === segment.end || (bounded === segment.start && preferAfter)) return { node: parent, offset: index + 1 }
      if (bounded > segment.start && bounded < segment.end) return preferAfter
        ? { node: parent, offset: index + 1 }
        : { node: parent, offset: index }
    }
    if (segment.kind === 'text') {
      if (bounded >= segment.start && bounded <= segment.end) return { node: segment.node, offset: bounded - segment.start }
    }
    if (segment.kind === 'break' && bounded === segment.start) return { node: parent, offset: index }
  }
  const last = editor.lastChild
  if (last?.nodeType === Node.TEXT_NODE) return { node: last, offset: last.textContent?.length || 0 }
  return { node: editor, offset: editor.childNodes.length }
}

function setCaretOffset(offset: number, preferAfter = false) {
  const target = boundaryAt(offset, preferAfter)
  const editor = editorRef.value
  if (!target || !editor) return
  const selection = window.getSelection()
  if (!selection) return
  const range = document.createRange()
  range.setStart(target.node, Math.max(0, target.offset))
  range.collapse(true)
  selection.removeAllRanges()
  selection.addRange(range)
}

function saveSelection() {
  const editor = editorRef.value
  const selection = window.getSelection()
  if (!editor || !selection || !selection.rangeCount) return
  const range = selection.getRangeAt(0)
  if (editor.contains(range.startContainer)) savedSelection.value = range.cloneRange()
}

function restoreSelection() {
  const editor = editorRef.value
  const selection = window.getSelection()
  const range = savedSelection.value
  if (!editor || !selection || !range || !editor.contains(range.startContainer) || !editor.contains(range.endContainer)) {
    setCaretOffset(contentTextFromDOM().length)
    return
  }
  selection.removeAllRanges()
  selection.addRange(range)
}

function focusEditor(restore = true) {
  nextTick(() => {
    editorRef.value?.focus()
    if (restore) restoreSelection()
  })
}

function rangeForOffsets(start: number, end: number): Range | null {
  const from = boundaryAt(start)
  const to = boundaryAt(end, true)
  if (!from || !to) return null
  const range = document.createRange()
  try {
    range.setStart(from.node, from.offset)
    range.setEnd(to.node, to.offset)
  } catch {
    return null
  }
  return range
}

function replaceOffsets(start: number, end: number, node?: Node): boolean {
  const range = rangeForOffsets(start, end)
  if (!range) return false
  range.deleteContents()
  if (node) range.insertNode(node)
  const offset = node ? start + segmentText(node).length : start
  setCaretOffset(offset, true)
  saveSelection()
  return true
}

function closePalette(restoreFocus = true) {
  commandPaletteOpen.value = false
  commandPaletteFromSlash.value = false
  commandPaletteQuery.value = ''
  slashTokenRef.value = null
  if (restoreFocus) focusEditor()
}

function closeAttachmentMenu() {
  attachmentMenuOpen.value = false
}

function openPalette() {
  if (props.disabled || props.activeRun) return
  closeAttachmentMenu()
  saveSelection()
  commandPaletteFromSlash.value = false
  commandPaletteQuery.value = ''
  commandPaletteOpen.value = true
}

function toggleAttachmentMenu() {
  if (props.disabled || props.activeRun) return
  closePalette(false)
  attachmentMenuOpen.value = !attachmentMenuOpen.value
}

function openAttachmentPicker() {
  if (props.disabled || props.activeRun) return
  closeAttachmentMenu()
  const input = attachmentInputRef.value
  if (!input) return
  input.value = ''
  waitForPicker()
  input.click()
}

function attachmentError(file: File, message: string): AssistantAttachmentChip {
  return {
    clientID: `attachment:${Date.now()}:${Math.random().toString(36).slice(2)}`,
    file,
    status: 'error',
    error: message,
  }
}

function appendAttachmentError(file: File, message: string) {
  if (attachmentChips.value.length >= MAX_ASSISTANT_COMPOSER_PARTS) return
  attachmentChips.value = [...attachmentChips.value, attachmentError(file, message)]
  emitAttachmentPending()
}

async function uploadAttachment(file: File, existingClientID?: string, allowWhileInactive = false) {
  // Capture the project before any await. A route switch can clear the chips
  // and update props while the upload is in flight; cleanup must still target
  // the project that accepted the original upload request.
  const projectName = props.projectName
  if ((props.disabled || props.activeRun) && !allowWhileInactive) return
  if (!projectName.trim()) {
    appendAttachmentError(file, 'Select a project before adding an attachment.')
    return
  }
  if (!assistantAttachmentIsSupported(file)) {
    appendAttachmentError(file, 'Only PNG, JPEG, WebP screenshots and .txt or .md files can be attached.')
    return
  }
  const existingChip = existingClientID
    ? attachmentChips.value.find((candidate) => candidate.clientID === existingClientID)
    : undefined
  const clientID = existingClientID || `attachment:${Date.now()}:${Math.random().toString(36).slice(2)}`
  const validationCandidates = existingChip
    ? attachmentChips.value.filter((candidate) => candidate.clientID !== existingClientID)
    : attachmentChips.value
  const sharedValidationError = assistantAttachmentValidationError(file, validationCandidates)
  if (sharedValidationError) {
    appendAttachmentError(file, sharedValidationError)
    return
  }
  const unresolvedCount = attachmentChips.value.filter((candidate) => candidate.clientID !== clientID && candidate.status !== 'ready').length
  if (localParts.value.length + unresolvedCount >= MAX_ASSISTANT_COMPOSER_PARTS) {
    appendAttachmentError(file, `A turn can contain at most ${MAX_ASSISTANT_COMPOSER_PARTS} content parts.`)
    return
  }
  const controller = new AbortController()
  const chip: AssistantAttachmentChip = existingChip || { clientID, file, projectName, status: 'uploading', retryAction: 'upload', controller }
  chip.file = file
  chip.projectName = projectName
  chip.committed = false
  chip.status = 'uploading'
  chip.error = undefined
  chip.retryAction = 'upload'
  chip.controller = controller
  if (!existingChip) attachmentChips.value = [...attachmentChips.value, chip]
  emitAttachmentPending()
  try {
    const receipt = projectAssistantAttachmentReceipt(await api.uploadAssistantAttachment(props.ctx, projectName, file, controller.signal, clientID))
    if (!receipt) throw new Error('The attachment upload returned an invalid receipt.')
    if (props.projectName !== projectName) {
      void bestEffortDeleteAttachment({ ...chip, receipt }, projectName)
      return
    }
    const current = attachmentChips.value.find((candidate) => candidate.clientID === clientID)
    if (!current) {
      void bestEffortDeleteAttachment({ ...chip, receipt }, projectName)
      return
    }
    current.receipt = receipt
    current.status = 'ready'
    current.controller = undefined
    current.error = undefined
    current.retryAction = undefined
    localParts.value = [...localParts.value, assistantAttachmentPart(receipt)]
    emitState()
  } catch (error) {
    const current = attachmentChips.value.find((candidate) => candidate.clientID === clientID)
    if (isAttachmentAbortError(error)) {
      void bestEffortDeleteAttachment(chip, projectName)
      if (current && props.projectName === projectName) {
        current.status = 'staged'
        current.controller = undefined
        current.error = undefined
        current.retryAction = undefined
        emitAttachmentPending()
      }
      return
    }
    // Preserve the File candidate after an ambiguous response. The server may
    // have created a draft even when the browser received an error.
    void bestEffortDeleteAttachment(chip, projectName)
    if (!current || props.projectName !== projectName) return
    current.status = 'error'
    current.controller = undefined
    current.error = error instanceof Error ? error.message : 'Attachment upload failed.'
    current.retryAction = 'upload'
    emitAttachmentPending()
  }
}

interface UnavailableAttachmentRecovery {
  recovered: number
  removed: number
  unresolved: number
}

/**
 * Replace precise server-rejected receipts with fresh uploads from the
 * browser-owned File candidates. Receipt-only chips cannot be reconstructed,
 * so they are removed and the parent leaves the authored prompt ready for an
 * explicit reattach. This method intentionally does not submit a turn.
 */
async function recoverUnavailableAttachments(receiptIDs: readonly string[]): Promise<UnavailableAttachmentRecovery> {
  const requestedIDs = new Set(receiptIDs.map((id) => id.trim()).filter(Boolean))
  if (!requestedIDs.size) return { recovered: 0, removed: 0, unresolved: 0 }
  const projectName = props.projectName
  let recovered = 0
  let removed = 0
  let unresolved = 0

  for (const chip of [...attachmentChips.value]) {
    const receiptID = chip.receipt?.id.trim() || ''
    if (!receiptID || !requestedIDs.has(receiptID)) continue
    const previousReceiptID = receiptID
    const replacementFile = chip.file
    localParts.value = localParts.value.filter((part) => part.type !== 'attachment' || part.attachment.id !== previousReceiptID)
    if (!replacementFile) {
      attachmentChips.value = attachmentChips.value.filter((candidate) => candidate.clientID !== chip.clientID)
      removed += 1
      emitState()
      continue
    }

    chip.receipt = undefined
    chip.status = 'staged'
    chip.error = undefined
    chip.retryAction = undefined
    chip.controller = undefined
    emitState()
    await uploadAttachment(replacementFile, chip.clientID, true)
    if (props.projectName !== projectName) return { recovered, removed, unresolved: unresolved + 1 }
    const replacement = attachmentChips.value.find((candidate) => candidate.clientID === chip.clientID)
    if (replacement?.status === 'ready' && replacement.receipt) recovered += 1
    else unresolved += 1
  }
  return { recovered, removed, unresolved }
}

/**
 * Transfer selected receipts to the accepted turn before the host clears its
 * contentParts prop. This is intentionally an exposed imperative seam: the
 * host's POST acceptance continuation is the only layer that knows when a
 * submission crossed the durable boundary.
 */
function commitAttachments(receiptIDs: readonly string[]) {
  const committedIDs = new Set(receiptIDs.map((id) => id.trim()).filter(Boolean))
  if (!committedIDs.size) return
  for (const chip of attachmentChips.value) {
    if (chip.status === 'ready' && chip.receipt && committedIDs.has(chip.receipt.id)) chip.committed = true
  }
}

function handleAttachmentInput(event: Event) {
  const input = event.target instanceof HTMLInputElement ? event.target : null
  const files = input?.files ? Array.from(input.files) : []
  void (async () => {
    for (const file of files) await uploadAttachment(file)
  })()
  if (input) input.value = ''
  restorePickerFocus()
}

function retryAttachment(chip: AssistantAttachmentChip) {
  if (chip.status === 'uploading' || chip.status === 'deleting') return
  if (!chip.retryAction) return
  if (chip.retryAction === 'delete') {
    void removeAttachment(chip)
    return
  }
  if (!chip.file) return
  // Keep the stable client identity across retries so a lost upload response
  // cannot turn one browser candidate into multiple server drafts.
  chip.status = 'staged'
  chip.error = undefined
  chip.retryAction = undefined
  emitAttachmentPending()
  void uploadAttachment(chip.file, chip.clientID)
}

async function removeAttachment(chip: AssistantAttachmentChip) {
  if (chip.status === 'uploading') {
    chip.controller?.abort()
    void bestEffortDeleteAttachment(chip, props.projectName)
    attachmentChips.value = attachmentChips.value.filter((candidate) => candidate.clientID !== chip.clientID)
    emitAttachmentPending()
    return
  }
  if (chip.status === 'deleting') return
  if (!chip.receipt) void bestEffortDeleteAttachment(chip, props.projectName)
  if (chip.receipt) {
    chip.status = 'deleting'
    chip.error = undefined
    chip.retryAction = undefined
    emitAttachmentPending()
    try {
      await api.deleteAssistantAttachment(props.ctx, props.projectName, chip.receipt.id)
    } catch (error) {
      if (isProjectAPINotFoundError(error)) {
        // DELETE is idempotent from the composer perspective: an expired or
        // already-removed draft is no longer present and can leave the UI.
      } else {
        chip.status = 'error'
        chip.error = error instanceof Error ? error.message : 'Attachment removal failed.'
        chip.retryAction = 'delete'
        emitAttachmentPending()
        return
      }
    }
  }
  if (chip.receipt) {
    localParts.value = localParts.value.filter((part) => part.type !== 'attachment' || part.attachment.id !== chip.receipt?.id)
  }
  attachmentChips.value = attachmentChips.value.filter((candidate) => candidate.clientID !== chip.clientID)
  emitState()
}

function detectSlash() {
  if (props.disabled || props.activeRun || composing.value) {
    if (commandPaletteOpen.value) closePalette(false)
    return
  }
  const caret = caretOffset()
  if (caret === null) return
  const token = assistantSlashToken(visibleTextFromDOM(), caret)
  if (!token) {
    if (commandPaletteFromSlash.value) closePalette(false)
    return
  }
  slashTokenRef.value = token
  commandPaletteFromSlash.value = true
  commandPaletteQuery.value = token.query
  commandPaletteOpen.value = true
  saveSelection()
}

function replaceSlashWithPart(part: Exclude<ProjectAssistantContentPart, { type: 'text' | 'annotation' | 'attachment' }>) {
  const token = slashTokenRef.value
  let start = token?.start
  let end = token?.end
  if (!token) {
    restoreSelection()
    const selection = selectionOffsets()
    if (!selection) return
    start = selection[0]
    end = selection[1]
  }
  const chip = createChip(part)
  if (start === undefined || end === undefined || !replaceOffsets(start, end, chip)) return
  emitState()
  closePalette()
}

function selectMode(mode: ProjectAssistantRunMode) {
  const token = slashTokenRef.value
  if (token) {
    const content = visibleTextFromDOM()
    replaceOffsets(token.start, token.end)
    // Keep the exact surrounding text. `consumeAssistantSlashToken` remains
    // available for non-DOM callers; the rich editor's range deletion is the
    // authoritative operation and never rebuilds unrelated chips.
    void consumeAssistantSlashToken(content, token)
    emitState()
  }
  emit('selectMode', mode)
  closePalette()
}

function chooseSkill(skill: ProjectAssistantSkill) {
  if (localSkills.value.some((candidate) => candidate.id === skill.id) || localSkills.value.length >= MAX_CHIPS) return
  localSkills.value = [...localSkills.value, skill]
  replaceSlashWithPart({ type: 'skill', skillID: skill.id })
}

function chooseResource(resource: ProjectAssistantContextResource) {
  if (localResources.value.some((candidate) => assistantResourceSelectionKey(candidate) === assistantResourceSelectionKey(resource)) || localResources.value.length >= MAX_CHIPS) return
  const resourceIndex = localResources.value.length
  localResources.value = [...localResources.value, resource]
  replaceSlashWithPart({ type: 'resource', resourceIndex })
}

function removeChip(chip: HTMLElement, placeAfter = false) {
  const segments = collectSegments()
  const segment = segments.find((candidate) => candidate.node === chip)
  const offset = segment ? (placeAfter ? segment.end : segment.start) : 0
  const kind = chip.dataset.assistantChip
  if (kind === 'skill' && chip.dataset.skillID) {
    localSkills.value = localSkills.value.filter((skill) => skill.id !== chip.dataset.skillID)
  } else if (kind === 'resource' && chip.dataset.resourceIndex) {
    const removedIndex = Number(chip.dataset.resourceIndex)
    if (Number.isSafeInteger(removedIndex) && removedIndex >= 0) {
      localResources.value = localResources.value.filter((_, index) => index !== removedIndex)
      for (const candidate of Array.from(editorRef.value?.querySelectorAll<HTMLElement>('[data-assistant-chip="resource"]') || [])) {
        const index = Number(candidate.dataset.resourceIndex)
        if (Number.isSafeInteger(index) && index > removedIndex) candidate.dataset.resourceIndex = String(index - 1)
      }
    }
  }
  chip.remove()
  emitState()
  setCaretOffset(offset, placeAfter)
}

function adjacentChip(offset: number, direction: 'before' | 'after'): HTMLElement | null {
  const segment = collectSegments().find((candidate) => candidate.kind === 'chip' && (direction === 'before' ? candidate.end === offset : candidate.start === offset))
  return segment?.node instanceof HTMLElement ? segment.node : null
}

function handleKeydown(event: KeyboardEvent) {
  if (props.disabled || event.defaultPrevented) return
  if (commandPaletteOpen.value && event.key === 'Enter') {
    event.preventDefault()
    return
  }
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault()
    if (!commandPaletteOpen.value) {
      const steer = props.activeRun && ((event.metaKey || event.ctrlKey) || !props.queueingEnabled)
      emit('submit', emitState(), steer ? 'steer' : 'queue')
    }
    return
  }
  if (!event.ctrlKey && !event.metaKey && !event.altKey) {
    const offset = caretOffset()
    if (offset !== null && event.key === 'Backspace') {
      const chip = adjacentChip(offset, 'before')
      if (chip) {
        event.preventDefault()
        removeChip(chip)
        return
      }
    }
    if (offset !== null && event.key === 'Delete') {
      const chip = adjacentChip(offset, 'after')
      if (chip) {
        event.preventDefault()
        removeChip(chip, true)
        return
      }
    }
    if (offset !== null && event.key === 'ArrowLeft') {
      const chip = adjacentChip(offset, 'before')
      if (chip) {
        event.preventDefault()
        const segment = collectSegments().find((candidate) => candidate.node === chip)
        if (segment) setCaretOffset(segment.start)
      }
    } else if (offset !== null && event.key === 'ArrowRight') {
      const chip = adjacentChip(offset, 'after')
      if (chip) {
        event.preventDefault()
        const segment = collectSegments().find((candidate) => candidate.node === chip)
        if (segment) setCaretOffset(segment.end, true)
      }
    }
  }
}

function handleInput() {
  if (suppressNextInput.value) {
    suppressNextInput.value = false
    return
  }
  emitState()
  detectSlash()
}

function handlePaste(event: ClipboardEvent) {
  event.preventDefault()
  const image = Array.from(event.clipboardData?.items || [])
    .find((item) => item.kind === 'file' && item.type.trim().toLowerCase().startsWith('image/'))
    ?.getAsFile()
  if (image) {
    void uploadAttachment(image.name ? image : new File([image], 'clipboard.png', { type: image.type || 'image/png' }))
    closePalette(false)
    return
  }
  const text = event.clipboardData?.getData('text/plain') || ''
  if (!text) return
  if (new TextEncoder().encode(text).byteLength > ASSISTANT_LARGE_PASTE_BYTES) {
    void uploadAttachment(new File([text], 'pasted-text.txt', { type: 'text/plain' }))
    closePalette(false)
    return
  }
  const offsets = selectionOffsets()
  if (!offsets) return
  const [start] = offsets
  const range = rangeForOffsets(start, offsets[1])
  if (!range) return
  const selection = window.getSelection()
  if (!selection?.rangeCount) return
  range.deleteContents()
  range.insertNode(document.createTextNode(text))
  setCaretOffset(start + text.length, true)
  saveSelection()
  suppressNextInput.value = true
  emitState()
  closePalette(false)
}

function handleCompositionStart() {
  composing.value = true
  if (commandPaletteOpen.value) closePalette(false)
}

function handleCompositionEnd() {
  composing.value = false
  nextTick(() => {
    emitState()
    detectSlash()
  })
}

function handleClick(event: MouseEvent) {
  const target = event.target instanceof HTMLElement ? event.target.closest<HTMLElement>('[data-assistant-chip]') : null
  if (!target || !editorRef.value?.contains(target)) return
  event.preventDefault()
  removeChip(target, true)
}

function syncFromProps() {
  const nextSkills = props.selectedSkills.slice(0, MAX_CHIPS)
  const nextResources = props.selectedResources.slice(0, MAX_CHIPS)
  const parts = normalizedParts()
  const signature = stateSignature(props.modelValue, parts, nextSkills, nextResources)
  localSkills.value = nextSkills
  localResources.value = nextResources
  reconcileAttachmentChips(parts)
  if (signature !== lastRenderedSignature.value) {
    localParts.value = parts
    renderParts(parts, props.modelValue, nextSkills, nextResources)
  }
}

watch(() => [props.modelValue, partSignature(props.contentParts), props.selectedSkills.map((skill) => skill.id).join(','), props.selectedResources.map(assistantResourceSelectionKey).join(',')], syncFromProps)
watch(() => props.projectName, (current, previous) => {
  if (current === previous) return
  cleanupAttachmentChips(attachmentChips.value, previous)
  attachmentChips.value = []
  emitAttachmentPending()
})
watch(() => [props.disabled, props.activeRun], ([disabled, active]) => {
  if (disabled || active) {
    closePalette(false)
    closeAttachmentMenu()
  }
})

onMounted(() => {
  syncFromProps()
  document.addEventListener('selectionchange', saveSelection)
})

onBeforeUnmount(() => {
  document.removeEventListener('selectionchange', saveSelection)
  cleanupAttachmentChips(attachmentChips.value, props.projectName)
})

defineExpose({
  focus: () => focusEditor(false),
  openPalette,
  closePalette,
  recoverUnavailableAttachments,
  commitAttachments,
})
</script>

<template>
  <div ref="rootRef" class="relative min-h-[72px]">
    <AssistantCommandPalette
      :open="commandPaletteOpen"
      :command-query="commandPaletteQuery"
      :preserve-composer-focus="commandPaletteFromSlash"
      :ctx="ctx"
      :providers="providers"
      :skills="skills"
      :selected-skill-i-ds="selectedSkillIDs"
      :selected-resources="localResources"
      @close="closePalette"
      @select-skill="chooseSkill"
      @select-resource="chooseResource"
      @select-mode="selectMode"
    />
    <div v-if="attachmentChips.length" class="relative z-10 flex flex-wrap gap-1.5 px-3 pt-2.5">
      <template v-for="chip in attachmentChips" :key="chip.clientID">
        <AssistantAttachmentPreview
          v-if="chip.file && assistantAttachmentIsImage(chip.file)"
          :file="chip.file"
          :label="attachmentLabel(chip)"
          :status="chip.status"
          :error="chip.error"
          :retryable="chip.status === 'error' && !!chip.retryAction"
          :retry-action="chip.retryAction"
          @retry="retryAttachment(chip)"
          @remove="removeAttachment(chip)"
        />
        <div
          v-else
          class="flex min-w-0 max-w-full flex-col items-stretch rounded-sm border px-2 py-1 text-[11px] font-mono"
          :class="chip.status === 'error'
            ? 'border-danger/40 bg-danger-subtle text-danger'
            : chip.status === 'ready'
              ? 'border-accent/30 bg-accent/10 text-accent'
              : 'border-border-subtle bg-surface-raised text-text-secondary'"
          :title="chip.error || attachmentStatusLabel(chip)"
        >
          <div class="flex min-w-0 items-center gap-1.5">
            <Loader2 v-if="chip.status === 'uploading' || chip.status === 'deleting'" class="h-3 w-3 shrink-0 animate-spin" :stroke-width="1.75" />
            <Image v-else-if="chip.receipt?.contentType.startsWith('image/') || (chip.file && assistantAttachmentIsImage(chip.file))" class="h-3 w-3 shrink-0" :stroke-width="1.75" />
            <FileText v-else class="h-3 w-3 shrink-0" :stroke-width="1.75" />
            <span class="max-w-48 truncate">{{ attachmentLabel(chip) }}</span>
            <span class="text-[10px] opacity-75">{{ attachmentStatusLabel(chip) }}</span>
            <div class="ml-auto flex shrink-0 items-center gap-0.5">
              <button
                v-if="chip.status === 'error' && chip.retryAction"
                type="button"
                class="app-studio-touch-target inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
                :aria-label="chip.retryAction === 'delete' ? `Retry attachment removal ${attachmentLabel(chip)}` : `Retry attachment upload ${attachmentLabel(chip)}`"
                :title="chip.retryAction === 'delete' ? 'Retry removal' : 'Retry upload'"
                @click="retryAttachment(chip)"
              >
                <RotateCcw class="h-3 w-3" :stroke-width="1.75" />
              </button>
              <button
                v-if="chip.status !== 'deleting'"
                type="button"
                class="app-studio-touch-target inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
                :aria-label="`Remove attachment ${attachmentLabel(chip)}`"
                title="Remove attachment"
                @click="removeAttachment(chip)"
              >
                <X class="h-3 w-3" :stroke-width="1.75" />
              </button>
            </div>
          </div>
          <AssistantAttachmentTextPreview
            v-if="chip.file && !assistantAttachmentIsImage(chip.file)"
            :file="chip.file"
            :label="attachmentLabel(chip)"
          />
        </div>
      </template>
    </div>
    <div v-if="localAnnotations.length" class="relative z-10 px-3 pt-2.5">
      <AssistantMessageAnnotations
        :annotations="localAnnotations"
        :current-document-id="annotationDocumentId"
        :current-page-path="annotationPagePath"
        :unresolved-annotation-ids="unresolvedAnnotationIds"
        :rebind-across-documents="true"
        :clearable="true"
        disclosure-id="assistant-composer-annotations"
        @remove-all="removeAllAnnotations"
      />
    </div>
    <div
      ref="editorRef"
      role="textbox"
      aria-multiline="true"
      :aria-label="placeholder"
      :data-placeholder="placeholder"
      :contenteditable="disabled ? 'false' : 'true'"
      class="assistant-rich-composer min-h-[72px] w-full whitespace-pre-wrap break-words rounded-md border-0 bg-transparent px-3 py-2.5 pb-12 pr-14 text-[13px] leading-5 text-text-primary outline-none empty:before:pointer-events-none empty:before:text-text-muted empty:before:content-[attr(data-placeholder)]"
      @keydown="handleKeydown"
      @input="handleInput"
      @paste="handlePaste"
      @compositionstart="handleCompositionStart"
      @compositionend="handleCompositionEnd"
      @click="handleClick"
      @focus="saveSelection"
    />
    <input
      ref="attachmentInputRef"
      type="file"
      class="hidden"
      :accept="ASSISTANT_ATTACHMENT_ACCEPT"
      multiple
      @change="handleAttachmentInput"
    />
    <div class="absolute bottom-2 left-1.5 right-12 flex min-w-0 items-center gap-2">
      <div class="flex min-w-0 items-center gap-0.5">
        <div ref="addMenuRootRef" class="contents">
          <div v-if="attachmentMenuOpen" class="absolute bottom-11 left-1 [z-index:var(--app-studio-z-menu)] min-w-48 rounded-md border border-border-default bg-surface-overlay p-1.5 shadow-lg" role="menu" aria-label="Add">
            <div class="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Add</div>
            <button
              type="button"
              class="app-studio-touch-target flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-[12px] text-text-secondary hover:bg-surface-hover hover:text-text-primary"
              role="menuitem"
              @click="openAttachmentPicker"
            >
              <Paperclip class="h-3.5 w-3.5" :stroke-width="1.75" />
              Files
            </button>
            <button
              type="button"
              class="app-studio-touch-target flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-[12px] text-text-secondary hover:bg-surface-hover hover:text-text-primary"
              role="menuitem"
              @click="openPalette"
            >
              <Upload class="h-3.5 w-3.5" :stroke-width="1.75" />
              Skill, resource, or command
            </button>
          </div>
          <button
            ref="attachmentMenuTriggerRef"
            type="button"
            class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-45"
            :disabled="disabled"
            title="Add files, skill, resource, or command"
            aria-label="Add files or open slash commands"
            aria-haspopup="menu"
            :aria-expanded="attachmentMenuOpen"
            @click="toggleAttachmentMenu"
          >
            <Plus class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </div>
        <slot name="controls" />
      </div>
      <div class="ml-auto min-w-0 shrink">
        <slot name="actions" />
      </div>
    </div>
  </div>
</template>
