<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeft, Boxes, ChevronRight, ClipboardList, Hash, Loader2, Plug, Search, TriangleAlert } from 'lucide-vue-next'
import {
  filterAssistantSlashCommands,
  type AssistantSlashCommand,
} from './assistantCommandPalette'
import {
  assistantResourceProviders,
  assistantResourceSelectionKey,
  discoverAssistantResources,
  type AssistantResourceGroup,
  type AssistantResourceInstance,
} from './assistantResources'
import { filterAssistantSkills } from './skillsSearch'
import type {
  FarosContext,
  ProjectAssistantContextResource,
  ProjectAssistantRunMode,
  ProjectAssistantSkill,
  ProviderItem,
} from './types'

type PaletteView = 'commands' | 'skills' | 'providers' | 'resources'

const props = defineProps<{
  open: boolean
  commandQuery: string
  ctx: FarosContext | null
  providers: ProviderItem[]
  skills: ProjectAssistantSkill[]
  selectedSkillIDs: string[]
  selectedResources: ProjectAssistantContextResource[]
}>()

const emit = defineEmits<{
  close: []
  selectSkill: [skill: ProjectAssistantSkill]
  selectResource: [resource: ProjectAssistantContextResource]
  selectMode: [mode: ProjectAssistantRunMode]
}>()

const view = ref<PaletteView>('commands')
const query = ref('')
const activeIndex = ref(0)
const searchRef = ref<HTMLInputElement | null>(null)
const selectedProviderName = ref('')
const resourceGroups = ref<AssistantResourceGroup[]>([])
const resourceWarnings = ref<string[]>([])
const resourceLoading = ref(false)
let resourceLoadSerial = 0

const resourceProviders = computed(() => assistantResourceProviders(props.providers))
const selectedProvider = computed(() => resourceProviders.value.find((provider) => provider.name === selectedProviderName.value))
const commands = computed(() => filterAssistantSlashCommands(props.commandQuery))
const skills = computed(() => filterAssistantSkills(props.skills.filter((skill) => skill.enabled !== false), query.value))
const providers = computed(() => {
  const normalized = query.value.trim().toLowerCase()
  if (!normalized) return resourceProviders.value
  return resourceProviders.value.filter((provider) => `${provider.displayName} ${provider.name}`.toLowerCase().includes(normalized))
})
const resources = computed(() => {
  const normalized = query.value.trim().toLowerCase()
  return resourceGroups.value.flatMap((group) => group.items.map((item) => ({ item, kind: group.type.kind })))
    .filter(({ item, kind }) => !normalized || `${kind} ${item.resourceRef.name}`.toLowerCase().includes(normalized))
})
const optionCount = computed(() => {
  if (view.value === 'commands') return commands.value.length
  if (view.value === 'skills') return skills.value.length
  if (view.value === 'providers') return providers.value.length
  return resources.value.length
})
const activeOptionID = computed(() => optionCount.value ? `assistant-command-option-${view.value}-${activeIndex.value}` : undefined)
const selectedResourceKeys = computed(() => new Set(props.selectedResources.map(assistantResourceSelectionKey)))

watch(() => props.open, (open) => {
  if (!open) return
  view.value = 'commands'
  query.value = ''
  activeIndex.value = 0
  selectedProviderName.value = ''
  resourceGroups.value = []
  resourceWarnings.value = []
})
watch([view, query, () => props.commandQuery], () => { activeIndex.value = 0 })

function optionDisabled(index: number): boolean {
  if (view.value === 'skills') {
    const skill = skills.value[index]
    return !skill || props.selectedSkillIDs.includes(skill.id) || props.selectedSkillIDs.length >= 8
  }
  if (view.value === 'resources') {
    const resource = resources.value[index]?.item
    return !resource || selectedResourceKeys.value.has(assistantResourceSelectionKey(resource)) || props.selectedResources.length >= 8
  }
  return false
}

function moveActive(delta: number) {
  const count = optionCount.value
  if (!count) return
  let next = activeIndex.value
  for (let attempts = 0; attempts < count; attempts++) {
    next = (next + delta + count) % count
    if (!optionDisabled(next)) break
  }
  activeIndex.value = next
  nextTick(() => document.getElementById(activeOptionID.value ?? '')?.scrollIntoView({ block: 'nearest' }))
}

function enterView(next: PaletteView) {
  view.value = next
  query.value = ''
  nextTick(() => searchRef.value?.focus())
}

function chooseCommand(command: AssistantSlashCommand) {
  if (command.id === 'skill') return enterView('skills')
  if (command.id === 'resource') return enterView('providers')
  emit('selectMode', command.id)
  emit('close')
}

async function chooseProvider(providerName: string) {
  const provider = resourceProviders.value.find((candidate) => candidate.name === providerName)
  if (!provider) return
  selectedProviderName.value = provider.name
  enterView('resources')
  const serial = ++resourceLoadSerial
  resourceLoading.value = true
  resourceGroups.value = []
  resourceWarnings.value = []
  const result = await discoverAssistantResources(props.ctx, provider.resourceTypes)
  if (serial !== resourceLoadSerial || selectedProviderName.value !== provider.name) return
  resourceGroups.value = result.groups
  resourceWarnings.value = result.warnings
  resourceLoading.value = false
}

function chooseSkill(skill: ProjectAssistantSkill) {
  if (props.selectedSkillIDs.includes(skill.id) || props.selectedSkillIDs.length >= 8) return
  emit('selectSkill', skill)
  emit('close')
}

function chooseResource(resource: AssistantResourceInstance) {
  if (selectedResourceKeys.value.has(assistantResourceSelectionKey(resource)) || props.selectedResources.length >= 8) return
  emit('selectResource', { provider: resource.provider, resourceRef: resource.resourceRef })
  emit('close')
}

function activateCurrent() {
  if (optionDisabled(activeIndex.value)) return
  if (view.value === 'commands') return commands.value[activeIndex.value] && chooseCommand(commands.value[activeIndex.value])
  if (view.value === 'skills') return skills.value[activeIndex.value] && chooseSkill(skills.value[activeIndex.value])
  if (view.value === 'providers') return providers.value[activeIndex.value] && void chooseProvider(providers.value[activeIndex.value].name)
  if (resources.value[activeIndex.value]) chooseResource(resources.value[activeIndex.value].item)
}

function back() {
  resourceLoadSerial++
  resourceLoading.value = false
  if (view.value === 'resources') return enterView('providers')
  if (view.value === 'skills' || view.value === 'providers') {
    view.value = 'commands'
    query.value = ''
    return
  }
  emit('close')
}

function handleKeydown(event: KeyboardEvent) {
  if (!props.open) return
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    event.stopPropagation()
    moveActive(event.key === 'ArrowDown' ? 1 : -1)
  } else if (event.key === 'Enter') {
    event.preventDefault()
    event.stopPropagation()
    activateCurrent()
  } else if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    back()
  }
}

onMounted(() => document.addEventListener('keydown', handleKeydown, true))
onBeforeUnmount(() => {
  resourceLoadSerial++
  document.removeEventListener('keydown', handleKeydown, true)
})
</script>

<template>
  <div v-if="open" class="fixed inset-0 z-40 bg-black/30 md:absolute md:inset-auto md:bottom-full md:left-0 md:mb-2 md:bg-transparent" @mousedown.self="emit('close')">
    <section
      class="fixed inset-x-2 bottom-2 z-50 flex max-h-[70vh] flex-col overflow-hidden rounded-lg border border-border-default bg-surface-raised shadow-2xl md:relative md:inset-auto md:bottom-auto md:w-[420px] md:max-h-[390px]"
      role="dialog"
      aria-label="Assistant slash commands"
    >
      <header class="flex min-h-10 items-center gap-1.5 border-b border-border-subtle px-2.5">
        <button v-if="view !== 'commands'" type="button" class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary" aria-label="Back" @click="back">
          <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
        </button>
        <div class="min-w-0 flex-1">
          <div class="text-[12px] font-semibold text-text-primary">
            {{ view === 'commands' ? 'Commands' : view === 'skills' ? 'Attach a skill' : view === 'providers' ? 'Choose a provider' : selectedProvider?.displayName || 'Choose a resource' }}
          </div>
          <div v-if="view === 'commands'" class="text-[10px] text-text-muted">Use ↑↓ to navigate · Enter to select · Esc to close</div>
        </div>
      </header>

      <div v-if="view !== 'commands'" class="border-b border-border-subtle p-1.5">
        <label class="relative block">
          <Search class="pointer-events-none absolute left-2.5 top-2 h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
          <input ref="searchRef" v-model="query" class="h-8 w-full rounded-md border border-border-subtle bg-surface pl-8 pr-2 text-[12px] text-text-primary outline-none focus:border-accent/50 focus:ring-2 focus:ring-accent/20" :placeholder="view === 'skills' ? 'Filter enabled skills' : view === 'providers' ? 'Filter providers' : 'Filter resources'" />
        </label>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto p-1.5 md:p-1" role="listbox" :aria-activedescendant="activeOptionID">
        <template v-if="view === 'commands'">
          <button v-for="(command, index) in commands" :id="`assistant-command-option-commands-${index}`" :key="command.id" type="button" role="option" :aria-selected="activeIndex === index" class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition md:py-1" :class="activeIndex === index ? 'bg-surface-hover text-text-primary' : 'text-text-secondary hover:bg-surface-hover'" @mouseenter="activeIndex = index" @click="chooseCommand(command)">
            <span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface">
              <Plug v-if="command.id === 'skill'" class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
              <Boxes v-else-if="command.id === 'resource'" class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
              <ClipboardList v-else class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
            </span>
            <span class="min-w-0 flex-1"><span class="block text-[13px] font-medium">/{{ command.id }}</span><span class="block truncate text-[11px] text-text-muted">{{ command.description }}</span></span>
            <ChevronRight v-if="command.id === 'skill' || command.id === 'resource'" class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
          </button>
        </template>

        <template v-else-if="view === 'skills'">
          <button v-for="(skill, index) in skills" :id="`assistant-command-option-skills-${index}`" :key="skill.id" type="button" role="option" :aria-selected="activeIndex === index" :aria-disabled="optionDisabled(index)" :disabled="optionDisabled(index)" class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-45 md:py-1" :class="activeIndex === index ? 'bg-surface-hover' : ''" @mouseenter="activeIndex = index" @click="chooseSkill(skill)">
            <Plug class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
            <span class="min-w-0 flex-1"><span class="block truncate text-[13px] font-medium text-text-primary">{{ skill.name }}</span><span class="block truncate text-[11px] text-text-muted">{{ skill.description || skill.scope }}</span></span>
            <span class="font-mono text-[9px] uppercase text-text-muted">{{ selectedSkillIDs.includes(skill.id) ? 'Added' : skill.scope }}</span>
          </button>
          <p v-if="!skills.length" class="px-3 py-8 text-center text-[12px] text-text-muted">No enabled skills match.</p>
        </template>

        <template v-else-if="view === 'providers'">
          <button v-for="(provider, index) in providers" :id="`assistant-command-option-providers-${index}`" :key="provider.name" type="button" role="option" :aria-selected="activeIndex === index" class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition hover:bg-surface-hover md:py-1" :class="activeIndex === index ? 'bg-surface-hover' : ''" @mouseenter="activeIndex = index" @click="chooseProvider(provider.name)">
            <span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface"><Boxes class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" /></span>
            <span class="min-w-0 flex-1"><span class="block truncate text-[13px] font-medium text-text-primary">{{ provider.displayName || provider.name }}</span><span class="block text-[11px] text-text-muted">{{ provider.resourceTypes.length }} resource {{ provider.resourceTypes.length === 1 ? 'type' : 'types' }}</span></span>
            <ChevronRight class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
          </button>
          <p v-if="!providers.length" class="px-3 py-8 text-center text-[12px] text-text-muted">No Ready providers publish usable actions.</p>
        </template>

        <template v-else>
          <div v-if="resourceLoading" class="flex items-center justify-center gap-2 px-3 py-8 text-[12px] text-text-muted"><Loader2 class="h-4 w-4 animate-spin" :stroke-width="1.75" />Loading resources…</div>
          <template v-else>
            <template v-for="({ item, kind }, index) in resources" :key="assistantResourceSelectionKey(item)">
              <div v-if="index === 0 || resources[index - 1]?.kind !== kind" class="px-2.5 pb-1 pt-2 font-mono text-[9px] font-semibold uppercase tracking-wide text-text-muted">{{ kind }}</div>
              <button :id="`assistant-command-option-resources-${index}`" type="button" role="option" :aria-selected="activeIndex === index" :aria-disabled="optionDisabled(index)" :disabled="optionDisabled(index)" class="flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-45 md:py-1" :class="activeIndex === index ? 'bg-surface-hover' : ''" @mouseenter="activeIndex = index" @click="chooseResource(item)">
                <Hash class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
                <span class="min-w-0 flex-1"><span class="block truncate font-mono text-[12px] text-text-primary">{{ item.resourceRef.name }}</span><span class="block truncate text-[10px] text-text-muted">{{ item.resourceRef.apiVersion }}</span></span>
                <span v-if="selectedResourceKeys.has(assistantResourceSelectionKey(item))" class="font-mono text-[9px] uppercase text-text-muted">Added</span>
              </button>
            </template>
            <p v-if="!resources.length" class="px-3 py-8 text-center text-[12px] text-text-muted">No accessible resources match.</p>
            <div v-for="warning in resourceWarnings" :key="warning" class="mx-2 mt-2 flex gap-2 rounded-md border border-warning/30 bg-warning-subtle px-2.5 py-2 text-[11px] text-warning"><TriangleAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />{{ warning }}</div>
          </template>
        </template>
      </div>
    </section>
  </div>
</template>
