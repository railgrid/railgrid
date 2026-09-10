<script setup lang="ts">
import { CalendarClock, Gauge, SlidersHorizontal, Wrench } from 'lucide-vue-next'
import { computed, ref, watch, type Component } from 'vue'
import AIWorkbenchLauncher from '../agentkit/AIWorkbenchLauncher.vue'
import type { AIWorkbenchLauncherItemView } from '../agentkit/ai'
import {
  canonicalAgentWorkbenchTab,
  type AgentWorkbenchBuiltInTab,
  type AgentWorkbenchTabDescriptor,
} from '../agent-workbench'

const props = defineProps<{
  tabs: readonly AgentWorkbenchTabDescriptor[]
  active?: boolean
}>()

const emit = defineEmits<{
  select: [tab: AgentWorkbenchBuiltInTab]
}>()

const builtInOrder: readonly AgentWorkbenchBuiltInTab[] = ['config', 'tools', 'automation', 'runs']
const query = ref('')
const normalizedQuery = computed(() => query.value.trim().toLowerCase())

watch(() => props.active, active => {
  if (active) query.value = ''
}, { immediate: true })

const existingTabs = computed<AIWorkbenchLauncherItemView[]>(() => props.tabs
  .filter(tab => tab.kind !== 'launcher' && matchesQuery(tab))
  .map(tab => launcherItem(tab)))

const suggestedItems = computed<AIWorkbenchLauncherItemView[]>(() => builtInOrder
  .filter(kind => !props.tabs.some(tab => tab.kind === kind))
  .map(kind => canonicalAgentWorkbenchTab(kind))
  .filter(tab => matchesQuery(tab))
  .map(tab => launcherItem(tab)))

function matchesQuery(tab: AgentWorkbenchTabDescriptor): boolean {
  if (!normalizedQuery.value) return true
  return `${tab.title} ${tab.subtitle ?? ''}`.toLowerCase().includes(normalizedQuery.value)
}

function tabIcon(kind: AgentWorkbenchBuiltInTab): Component {
  if (kind === 'config') return SlidersHorizontal
  if (kind === 'tools') return Wrench
  if (kind === 'automation') return CalendarClock
  return Gauge
}

function launcherItem(tab: AgentWorkbenchTabDescriptor): AIWorkbenchLauncherItemView {
  return {
    id: tab.id,
    title: tab.title,
    ...(tab.subtitle ? { subtitle: tab.subtitle } : {}),
    icon: tabIcon(tab.kind as AgentWorkbenchBuiltInTab),
  }
}

function select(id: string): void {
  if (builtInOrder.includes(id as AgentWorkbenchBuiltInTab)) emit('select', id as AgentWorkbenchBuiltInTab)
}
</script>

<template>
  <AIWorkbenchLauncher
    v-model:query="query"
    :existing-tabs="existingTabs"
    :suggested-items="suggestedItems"
    @select-existing="select"
    @select="select"
  />
</template>
