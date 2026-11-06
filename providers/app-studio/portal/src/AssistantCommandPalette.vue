<script setup lang="ts">
import { computed } from 'vue'
import AICommandPalette from './agentkit/AICommandPalette.vue'
import { filterAssistantSlashCommands } from './assistantCommandPalette'
import { assistantResourceProviders, discoverAssistantResources } from './assistantResources'
import type {
  ProjectAssistantContextResource,
  ProjectAssistantRunMode,
  ProjectAssistantSkill,
  ProviderItem,
  RailgridContext,
} from './types'
import type { AgentKitContextResource, AgentKitResourceProvider, AgentKitSkill } from './agentkit/composer'

const props = defineProps<{
  open: boolean
  anchor?: HTMLElement | null
  commandQuery: string
  preserveComposerFocus?: boolean
  ctx: RailgridContext | null
  providers: ProviderItem[]
  skills: ProjectAssistantSkill[]
  selectedSkillIDs: string[]
  selectedResources: ProjectAssistantContextResource[]
}>()

const emit = defineEmits<{
  close: [restoreFocus?: boolean]
  selectSkill: [skill: ProjectAssistantSkill]
  selectResource: [resource: ProjectAssistantContextResource]
  selectMode: [mode: ProjectAssistantRunMode]
}>()

const commands = computed(() => filterAssistantSlashCommands(props.commandQuery))
const resourceProviders = computed(() => assistantResourceProviders(props.providers))

function discoverResources(provider: AgentKitResourceProvider) {
  const match = resourceProviders.value.find((candidate) => candidate.name === provider.name)
  return discoverAssistantResources(props.ctx, match?.resourceTypes ?? [])
}

function selectSkill(skill: AgentKitSkill) {
  emit('selectSkill', skill as ProjectAssistantSkill)
}

function selectResource(resource: AgentKitContextResource) {
  emit('selectResource', { provider: resource.provider, resourceRef: resource.resourceRef })
}
</script>

<template>
  <AICommandPalette
    :open="open"
    :anchor="anchor"
    :command-query="commandQuery"
    :preserve-composer-focus="preserveComposerFocus"
    :commands="commands"
    :resource-providers="resourceProviders"
    :discover-resources="discoverResources"
    :skills="skills"
    :selected-skill-i-ds="selectedSkillIDs"
    :selected-resources="selectedResources"
    @close="emit('close', $event)"
    @select-skill="selectSkill"
    @select-resource="selectResource"
    @select-mode="emit('selectMode', $event as ProjectAssistantRunMode)"
  />
</template>
