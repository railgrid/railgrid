<script setup lang="ts">
import { CalendarClock, Gauge, GripVertical, Plus, SlidersHorizontal, Wrench } from 'lucide-vue-next'
import { computed, nextTick, onMounted, ref, watch, type Component } from 'vue'
import AIWorkbenchTabs from '../agentkit/AIWorkbenchTabs.vue'
import type { AIWorkbenchTabView } from '../agentkit/ai'
import type {
  AgentWorkbenchTabDescriptor,
  AgentWorkbenchTabDropPlacement,
  AgentWorkbenchTabKind,
} from '../agent-workbench'

const props = defineProps<{
  tabs: readonly AgentWorkbenchTabDescriptor[]
  active: AgentWorkbenchTabKind
}>()

const emit = defineEmits<{
  select: [tab: AgentWorkbenchTabKind]
  close: [tab: AgentWorkbenchTabKind]
  reorder: [draggedTab: AgentWorkbenchTabKind, targetTab: AgentWorkbenchTabKind, placement: AgentWorkbenchTabDropPlacement]
  'new-tab': []
}>()

const draggedTabID = ref<AgentWorkbenchTabKind | null>(null)
const dragOverTabID = ref<AgentWorkbenchTabKind | null>(null)
const dragOverPlacement = ref<AgentWorkbenchTabDropPlacement>('before')

function isActive(tab: AgentWorkbenchTabKind): boolean {
  return props.active === tab
}

const tabItems = computed<AIWorkbenchTabView[]>(() => props.tabs.map(tab => ({
  id: tab.id,
  controlId: tabID(tab.id),
  dataTabId: tab.id,
  title: tab.title,
  selected: isActive(tab.id),
  active: isActive(tab.id),
  dragged: draggedTabID.value === tab.id,
  dragOver: dragOverTabID.value === tab.id,
  dropPlacement: dragOverTabID.value === tab.id ? dragOverPlacement.value : undefined,
  draggable: true,
  controls: panelID(tab.id),
  tabindex: isActive(tab.id) ? 0 : -1,
  closeable: tab.closeable,
})))

function tabID(tab: AgentWorkbenchTabKind): string {
  return `agents-workbench-tab-${tab}`
}

function panelID(tab: AgentWorkbenchTabKind): string {
  return `agents-workbench-panel-${tab}`
}

function tabIcon(tab: string): Component {
  if (tab === 'config') return SlidersHorizontal
  if (tab === 'tools') return Wrench
  if (tab === 'automation') return CalendarClock
  if (tab === 'runs') return Gauge
  return Plus
}

function reveal(tab: AgentWorkbenchTabKind): void {
  if (typeof document === 'undefined') return
  const element = document.getElementById(tabID(tab))
  if (element && typeof element.scrollIntoView === 'function') {
    element.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }
}

function revealActive(): void {
  void nextTick(() => reveal(props.active))
}

onMounted(revealActive)
watch(() => props.active, revealActive, { flush: 'post' })
watch(() => props.tabs.length, revealActive, { flush: 'post' })

function select(tab: string): void {
  if (!props.tabs.some(item => item.id === tab)) return
  emit('select', tab as AgentWorkbenchTabKind)
  void nextTick(() => reveal(tab as AgentWorkbenchTabKind))
}

function close(tab: string): void {
  if (!props.tabs.some(item => item.id === tab)) return
  const wasActive = props.active === tab
  emit('close', tab as AgentWorkbenchTabKind)
  if (!wasActive) return
  void nextTick(() => {
    const active = props.active
    document.getElementById(tabID(active))?.focus()
    reveal(active)
  })
}

function move(tab: string, event: KeyboardEvent): void {
  const currentIndex = props.tabs.findIndex(item => item.id === tab)
  if (currentIndex < 0) return

  if (event.altKey && event.shiftKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
    const direction = event.key === 'ArrowLeft' ? -1 : 1
    const target = props.tabs[currentIndex + direction]
    if (!target) return
    event.preventDefault()
    emit('reorder', tab as AgentWorkbenchTabKind, target.id, direction < 0 ? 'before' : 'after')
    void nextTick(() => {
      document.getElementById(tabID(tab as AgentWorkbenchTabKind))?.focus()
      reveal(tab as AgentWorkbenchTabKind)
    })
    return
  }

  let nextIndex = currentIndex
  if (event.key === 'ArrowRight' || event.key === 'ArrowDown') nextIndex = (currentIndex + 1) % props.tabs.length
  else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') nextIndex = (currentIndex - 1 + props.tabs.length) % props.tabs.length
  else if (event.key === 'Home') nextIndex = 0
  else if (event.key === 'End') nextIndex = props.tabs.length - 1
  else return
  event.preventDefault()
  const next = props.tabs[nextIndex]?.id
  if (!next) return
  select(next)
  void nextTick(() => {
    document.getElementById(tabID(next))?.focus()
    reveal(next)
  })
}

function startDrag(tab: string, event: DragEvent): void {
  if (!props.tabs.some(item => item.id === tab)) return
  draggedTabID.value = tab as AgentWorkbenchTabKind
  dragOverTabID.value = null
  dragOverPlacement.value = 'before'
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', tab)
  }
}

function dragOver(tab: string, event: DragEvent): void {
  const dragged = draggedTabID.value
  if (!dragged || dragged === tab || !props.tabs.some(item => item.id === tab)) return
  event.preventDefault()
  dragOverTabID.value = tab as AgentWorkbenchTabKind
  dragOverPlacement.value = dropPlacement(event)
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
}

function drop(tab: string, event: DragEvent): void {
  event.preventDefault()
  const dragged = draggedTabID.value || event.dataTransfer?.getData('text/plain')
  if (dragged && dragged !== tab && props.tabs.some(item => item.id === dragged) && props.tabs.some(item => item.id === tab)) {
    emit('reorder', dragged as AgentWorkbenchTabKind, tab as AgentWorkbenchTabKind, dropPlacement(event))
  }
  clearDragState()
}

function clearDragState(): void {
  draggedTabID.value = null
  dragOverTabID.value = null
  dragOverPlacement.value = 'before'
}

function dropPlacement(event: DragEvent): AgentWorkbenchTabDropPlacement {
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return 'before'
  const rect = target.getBoundingClientRect()
  return event.clientX > rect.left + rect.width / 2 ? 'after' : 'before'
}
</script>

<template>
  <div class="agents-workbench-header" data-agent-workbench-header>
    <AIWorkbenchTabs
      :tabs="tabItems"
      class="agents-workbench-tabstrip"
      data-agent-workbench-tablist
      aria-label="Agent workbench sections"
      launcher
      @dragstart="startDrag"
      @dragover="dragOver"
      @drop="drop"
      @dragend="clearDragState"
      @select="select"
      @keydown="move"
      @close="close"
      @launch="emit('new-tab')"
    >
      <template #leading>
        <GripVertical :size="12" :stroke-width="1.75" aria-hidden="true" />
      </template>
      <template #icon="{ tab }"><component :is="tabIcon(tab.id)" :stroke-width="1.75" aria-hidden="true" /></template>
    </AIWorkbenchTabs>
  </div>
</template>
