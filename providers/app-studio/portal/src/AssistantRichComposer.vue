<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Plus } from 'lucide-vue-next'
import AssistantCommandPalette from './AssistantCommandPalette.vue'
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
import type {
  FarosContext,
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
  skills: ProjectAssistantSkill[]
  selectedSkills?: ProjectAssistantSkill[]
  selectedResources?: ProjectAssistantContextResource[]
  ctx: FarosContext | null
  providers: ProviderItem[]
  disabled?: boolean
  activeRun?: boolean
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
  state: [value: AssistantComposerState]
  submit: [value: AssistantComposerState]
  selectMode: [mode: ProjectAssistantRunMode]
}>()

const editorRef = ref<HTMLDivElement | null>(null)
const commandPaletteOpen = ref(false)
const commandPaletteFromSlash = ref(false)
const commandPaletteQuery = ref('')
const composing = ref(false)
const suppressNextInput = ref(false)
const lastRenderedSignature = ref('')
const savedSelection = ref<Range | null>(null)
const slashTokenRef = ref<ReturnType<typeof assistantSlashToken>>(null)
const localParts = ref<ProjectAssistantContentPart[]>([])
const localSkills = ref<ProjectAssistantSkill[]>([])
const localResources = ref<ProjectAssistantContextResource[]>([])

const localAnnotations = computed(() => localParts.value
  .filter((part): part is Extract<ProjectAssistantContentPart, { type: 'annotation' }> => part.type === 'annotation')
  .map((part) => part.annotation))

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

function createChip(part: Exclude<ProjectAssistantContentPart, { type: 'text' | 'annotation' }>): HTMLSpanElement {
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
    else if (part.type !== 'annotation') editor.append(createChip(part))
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
  // Preview annotations are attachments, not editable prose. Keep them out of
  // the contenteditable DOM so caret movement and chip deletion remain
  // predictable, then append their stable descriptors to the submitted turn.
  for (const part of localParts.value) {
    if (part.type === 'annotation') append(part)
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
  const state: AssistantComposerState = { content, contentParts: parts as AssistantComposerPart[], skills: [...localSkills.value], contextResources: [...localResources.value] }
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

function openPalette() {
  if (props.disabled || props.activeRun) return
  saveSelection()
  commandPaletteFromSlash.value = false
  commandPaletteQuery.value = ''
  commandPaletteOpen.value = true
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

function replaceSlashWithPart(part: Exclude<ProjectAssistantContentPart, { type: 'text' | 'annotation' }>) {
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
    if (!commandPaletteOpen.value) emit('submit', emitState())
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
  const text = event.clipboardData?.getData('text/plain') || ''
  if (!text) return
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
  if (signature !== lastRenderedSignature.value) {
    localParts.value = parts
    renderParts(parts, props.modelValue, nextSkills, nextResources)
  }
}

watch(() => [props.modelValue, partSignature(props.contentParts), props.selectedSkills.map((skill) => skill.id).join(','), props.selectedResources.map(assistantResourceSelectionKey).join(',')], syncFromProps)
watch(() => [props.disabled, props.activeRun], ([disabled, active]) => {
  if (disabled || active) closePalette(false)
})

onMounted(() => {
  syncFromProps()
  document.addEventListener('selectionchange', saveSelection)
})

onBeforeUnmount(() => {
  document.removeEventListener('selectionchange', saveSelection)
})

defineExpose({ focus: () => focusEditor(false), openPalette, closePalette })
</script>

<template>
  <div class="relative min-h-[72px]">
    <AssistantCommandPalette
      :open="commandPaletteOpen"
      :command-query="commandPaletteQuery"
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
    <div class="absolute bottom-2 left-1.5 right-12 flex min-w-0 items-center gap-0.5">
      <button
        type="button"
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-45"
        :disabled="disabled"
        title="Add skill, resource, or command"
        aria-label="Open slash commands"
        aria-haspopup="dialog"
        :aria-expanded="commandPaletteOpen"
        @click="openPalette"
      >
        <Plus class="h-4 w-4" :stroke-width="1.75" />
      </button>
      <slot name="controls" />
    </div>
  </div>
</template>
