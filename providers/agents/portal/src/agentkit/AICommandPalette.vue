<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowDown, ArrowLeft, ArrowUp, Boxes, ChevronRight, ClipboardList, Hash, Loader2, Plug, Search, TriangleAlert } from 'lucide-vue-next'
import { useAnchoredPopover } from '../portalkit/useAnchoredPopover'
import { agentKitResourceSelectionKey, filterAgentKitSlashCommands, type AgentKitContextResource, type AgentKitResourceDiscoveryResult, type AgentKitResourceGroup, type AgentKitResourceInstance, type AgentKitResourceProvider, type AgentKitSkill, type AgentKitSlashCommand } from './composer'
import { filterAssistantSkills } from './composerSkillsSearch'

type PaletteView = 'commands' | 'skills' | 'providers' | 'resources'
const emptyClass = 'px-3 py-8 text-center text-[12px] text-text-muted'
const optionClass = 'k-agentkit-touch-target flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition md:py-1'

const props = withDefaults(defineProps<{
  open: boolean
  anchor?: HTMLElement | null
  commandQuery: string
  preserveComposerFocus?: boolean
  commands: AgentKitSlashCommand[]
  resourceProviders: AgentKitResourceProvider[]
  discoverResources: (provider: AgentKitResourceProvider) => Promise<AgentKitResourceDiscoveryResult>
  skills: AgentKitSkill[]
  selectedSkillIDs: string[]
  selectedResources: AgentKitContextResource[]
}>(), {
  commands: () => [
    { id: 'skill', label: 'Skill', description: 'Attach an enabled skill to this turn' },
    { id: 'resource', label: 'Resource', description: 'Reference a provider resource' },
  ],
  resourceProviders: () => [],
  discoverResources: async () => ({ groups: [], warnings: [] }),
})

const emit = defineEmits<{
  close: [restoreFocus?: boolean]
  selectSkill: [skill: AgentKitSkill]
  selectResource: [resource: AgentKitContextResource]
  selectMode: [mode: string]
}>()

const view = ref<PaletteView>('commands')
const query = ref('')
const activeIndex = ref(0)
const searchRef = ref<HTMLInputElement | null>(null)
const listboxRef = ref<HTMLElement | null>(null)
const desktopViewport = ref(false)
const selectedProviderName = ref('')
const resourceGroups = ref<AgentKitResourceGroup[]>([])
const resourceWarnings = ref<string[]>([])
const resourceLoading = ref(false)
let resourceLoadSerial = 0
let focusing = false
let alive = true
let paletteOpener: HTMLElement | null = null
let externalFocusOwner: HTMLElement | null = null
const externalOwnerAttributes = new Map<string, string | null>()
const listboxID = crypto.randomUUID()
const externalARIAAttributes = ['role', 'aria-multiline', 'aria-autocomplete', 'aria-controls', 'aria-expanded', 'aria-haspopup', 'aria-activedescendant'] as const
const {
  open: positionTrackingOpen,
  triggerRef: anchorRef,
  panelRef,
  panelStyle,
  updatePosition,
} = useAnchoredPopover({ width: 420, gap: 8, viewportMargin: 8, align: 'start' })
const anchoredPanelStyle = computed<Record<string, string>>(() => ({
  position: panelStyle.value.position || 'fixed',
  top: panelStyle.value.top || '',
  left: panelStyle.value.left || '',
  maxWidth: panelStyle.value.maxWidth || '',
}))

const providers = computed(() => {
  const normalized = query.value.trim().toLowerCase()
  if (!normalized) return props.resourceProviders
  return props.resourceProviders.filter((provider) => `${provider.displayName} ${provider.name}`.toLowerCase().includes(normalized))
})
const selectedProvider = computed(() => props.resourceProviders.find((provider) => provider.name === selectedProviderName.value))
const commands = computed(() => filterAgentKitSlashCommands(props.commandQuery, props.commands))
const skills = computed(() => filterAssistantSkills(props.skills.filter((skill) => skill.enabled !== false), query.value))
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
const activeOptionID = computed(() => optionCount.value ? optionID(activeIndex.value) : undefined)
const selectedResourceKeys = computed(() => new Set(props.selectedResources.map(agentKitResourceSelectionKey)))

watch(() => props.open, (open) => {
  if (!open) {
    positionTrackingOpen.value = false
    releaseExternalFocusOwner()
    paletteOpener = null
    return
  }
  paletteOpener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  anchorRef.value = props.anchor?.isConnected ? props.anchor : paletteOpener
  positionTrackingOpen.value = true
  view.value = 'commands'
  query.value = ''
  activeIndex.value = 0
  selectedProviderName.value = ''
  resourceGroups.value = []
  resourceWarnings.value = []
  resourceLoadSerial++
  resourceLoading.value = false
  void focusCurrentOwner()
})
watch(() => props.anchor, anchor => {
  if (!props.open || !anchor?.isConnected) return
  anchorRef.value = anchor
  void nextTick(updatePosition)
})
watch([view, query, () => props.commandQuery, optionCount], () => {
  activeIndex.value = 0
  while (activeIndex.value < optionCount.value - 1 && optionDisabled(activeIndex.value)) activeIndex.value++
})
watch(activeOptionID, () => syncExternalActiveOption())

function releaseExternalFocusOwner() {
  if (!externalFocusOwner) return
  externalFocusOwner.removeEventListener('keydown', handleKeydown, true)
  externalFocusOwner.removeEventListener('focusout', handleFocusOut)
  for (const attribute of externalARIAAttributes) {
    const previous = externalOwnerAttributes.get(attribute)
    if (previous === null || previous === undefined) externalFocusOwner.removeAttribute(attribute)
    else externalFocusOwner.setAttribute(attribute, previous)
  }
  externalOwnerAttributes.clear()
  externalFocusOwner = null
}

function syncExternalActiveOption() {
  if (!externalFocusOwner) return
  if (activeOptionID.value) externalFocusOwner.setAttribute('aria-activedescendant', activeOptionID.value)
  else externalFocusOwner.removeAttribute('aria-activedescendant')
}

function bindExternalFocusOwner(owner: HTMLElement) {
  releaseExternalFocusOwner()
  externalFocusOwner = owner
  for (const attribute of externalARIAAttributes) externalOwnerAttributes.set(attribute, owner.getAttribute(attribute))
  owner.setAttribute('role', 'combobox')
  owner.removeAttribute('aria-multiline')
  owner.setAttribute('aria-autocomplete', 'list')
  owner.setAttribute('aria-controls', listboxID)
  owner.setAttribute('aria-expanded', 'true')
  owner.setAttribute('aria-haspopup', 'listbox')
  syncExternalActiveOption()
  owner.addEventListener('keydown', handleKeydown, true)
  owner.addEventListener('focusout', handleFocusOut)
}

async function focusCurrentOwner() {
  focusing = true
  releaseExternalFocusOwner()
  await nextTick()
  focusing = false
  if (!alive || !props.open) return
  if (view.value !== 'commands') {
    searchRef.value?.focus({ preventScroll: true })
    return
  }
  if (props.preserveComposerFocus && paletteOpener?.isConnected) {
    bindExternalFocusOwner(paletteOpener)
    paletteOpener.focus({ preventScroll: true })
    return
  }
  listboxRef.value?.focus({ preventScroll: true })
}

function hoverOption(index: number) { if (!optionDisabled(index)) activeIndex.value = index }

function optionID(index: number): string { return `${listboxID}-${view.value}-${index}` }

function optionDisabled(index: number): boolean {
  if (view.value === 'skills') {
    const skill = skills.value[index]
    return !skill || props.selectedSkillIDs.includes(skill.id) || props.selectedSkillIDs.length >= 8
  }
  if (view.value === 'resources') {
    const resource = resources.value[index]?.item
    return !resource || selectedResourceKeys.value.has(agentKitResourceSelectionKey(resource)) || props.selectedResources.length >= 8
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
  void focusCurrentOwner()
}

function commitSelection(commit: () => void) {
  focusing = true
  try { commit(); emit('close') }
  finally { focusing = false }
}

function chooseCommand(command: AgentKitSlashCommand) {
  if (command.id === 'skill') return enterView('skills')
  if (command.id === 'resource') return enterView('providers')
  commitSelection(() => emit('selectMode', command.id))
}

async function chooseProvider(providerName: string) {
  const provider = props.resourceProviders.find((candidate) => candidate.name === providerName)
  if (!provider) return
  selectedProviderName.value = provider.name
  enterView('resources')
  const serial = ++resourceLoadSerial
  resourceLoading.value = true
  resourceGroups.value = []
  resourceWarnings.value = []
  let result: AgentKitResourceDiscoveryResult
  try { result = await props.discoverResources(provider) }
  catch { result = { groups: [], warnings: ['Resources unavailable. Go back to retry.'] } }
  if (serial !== resourceLoadSerial || selectedProviderName.value !== provider.name) return
  resourceGroups.value = result.groups
  resourceWarnings.value = result.warnings
  resourceLoading.value = false
}

function chooseSkill(skill: AgentKitSkill) {
  if (props.selectedSkillIDs.includes(skill.id) || props.selectedSkillIDs.length >= 8) return
  commitSelection(() => emit('selectSkill', skill))
}

function chooseResource(resource: AgentKitResourceInstance) {
  if (selectedResourceKeys.value.has(agentKitResourceSelectionKey(resource)) || props.selectedResources.length >= 8) return
  commitSelection(() => emit('selectResource', { provider: resource.provider, resourceRef: resource.resourceRef }))
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
  if (view.value === 'skills' || view.value === 'providers') return enterView('commands')
  emit('close')
}

function handleFocusOut(event: FocusEvent) {
  const target = event.relatedTarget
  if (props.open && !focusing && !(target instanceof Node && (panelRef.value?.contains(target) || target === externalFocusOwner))) emit('close', false)
}

function isEditableKeyboardOwner(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && (
    target instanceof HTMLInputElement
    || target instanceof HTMLTextAreaElement
    || target.isContentEditable
  )
}

function handleKeydown(event: KeyboardEvent) {
  if (!props.open || event.isComposing || event.keyCode === 229) return
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    event.stopPropagation()
    moveActive(event.key === 'ArrowDown' ? 1 : -1)
  } else if (event.key === 'Home' || event.key === 'End') {
    // Home and End move the caret in the filter and slash composer. Only use
    // them for listbox navigation when the non-editable listbox owns focus.
    if (isEditableKeyboardOwner(event.currentTarget)) return
    event.preventDefault()
    event.stopPropagation()
    activeIndex.value = event.key === 'Home' ? 0 : Math.max(0, optionCount.value - 1)
    if (optionDisabled(activeIndex.value)) moveActive(event.key === 'Home' ? 1 : -1)
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

let viewportQuery: MediaQueryList | null = null
function onViewportChange(event: MediaQueryListEvent | MediaQueryList): void {
  desktopViewport.value = event.matches
}

onMounted(() => {
  if (typeof window.matchMedia === 'function') {
    viewportQuery = window.matchMedia('(min-width: 768px)')
    onViewportChange(viewportQuery)
    viewportQuery.addEventListener?.('change', onViewportChange)
  } else {
    desktopViewport.value = false
  }
  if (props.open) {
    paletteOpener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    anchorRef.value = props.anchor?.isConnected ? props.anchor : paletteOpener
    positionTrackingOpen.value = true
    void focusCurrentOwner()
  }
})
onBeforeUnmount(() => {
  alive = false
  viewportQuery?.removeEventListener?.('change', onViewportChange)
  resourceLoadSerial++
  releaseExternalFocusOwner()
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="k-ai-command-palette-backdrop" @mousedown.self="emit('close')">
    <section
      ref="panelRef"
      :style="desktopViewport ? anchoredPanelStyle : undefined"
      class="k-ai-command-palette-panel"
      @focusout="handleFocusOut"
      @keydown.esc="handleKeydown"
      :role="preserveComposerFocus && view === 'commands' ? undefined : 'dialog'"
      :aria-label="preserveComposerFocus && view === 'commands' ? undefined : 'Assistant slash commands'"
    >
      <header class="k-ai-command-palette__header">
        <button v-if="view !== 'commands'" type="button" class="k-agentkit-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary" aria-label="Back" @click="back">
          <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
        </button>
        <div class="min-w-0 flex-1">
          <div class="text-[12px] font-semibold text-text-primary">
            {{ view === 'commands' ? 'Commands' : view === 'skills' ? 'Attach a skill' : view === 'providers' ? 'Choose a provider' : selectedProvider?.displayName || 'Choose a resource' }}
          </div>
          <div v-if="view === 'commands'" class="flex items-center gap-1 text-[10px] text-text-muted">
            <span>Use</span>
            <ArrowUp class="h-2.5 w-2.5" :stroke-width="1.75" aria-hidden="true" />
            <ArrowDown class="h-2.5 w-2.5" :stroke-width="1.75" aria-hidden="true" />
            <span>to navigate · Enter to select · Esc to close</span>
          </div>
        </div>
      </header>

      <div v-if="view !== 'commands'">
        <label class="k-ai-command-palette__filter">
          <span class="k-ai-command-palette__filter-label">Filter</span>
          <span class="k-ai-search k-ai-search--compact">
            <Search class="k-ai-search__icon" :stroke-width="1.75" aria-hidden="true" />
            <input
              ref="searchRef"
              v-model="query"
              role="combobox"
              aria-autocomplete="list"
              :aria-controls="listboxID"
              aria-expanded="true"
              aria-haspopup="listbox"
              :aria-activedescendant="activeOptionID"
              class="k-input k-ai-search__input"
              :placeholder="view === 'skills' ? 'Enabled skills' : view === 'providers' ? 'Providers' : 'Resources'"
              @keydown="handleKeydown"
            />
          </span>
        </label>
      </div>

      <div
        :id="listboxID"
        ref="listboxRef"
        class="k-ai-command-palette__list"
        role="listbox"
        :aria-label="view === 'commands' ? 'Assistant commands' : view === 'skills' ? 'Enabled skills' : view === 'providers' ? 'Providers' : 'Resources'"
        :tabindex="view === 'commands' && !preserveComposerFocus ? 0 : -1"
        :aria-activedescendant="view === 'commands' && !preserveComposerFocus ? activeOptionID : undefined"
        @keydown="handleKeydown"
      >
        <template v-if="view === 'commands'">
          <p v-if="!commands.length" :class="emptyClass" role="status">No commands match.</p>
          <button v-for="(command, index) in commands" :id="optionID(index)" :key="command.id" type="button" role="option" tabindex="-1" :aria-selected="activeIndex === index" :class="[optionClass, activeIndex === index ? 'bg-surface-hover text-text-primary' : 'text-text-secondary hover:bg-surface-hover']" @mousedown.prevent @mouseenter="hoverOption(index)" @click="chooseCommand(command)">
            <span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface">
              <Plug v-if="command.id === 'skill'" class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              <Boxes v-else-if="command.id === 'resource'" class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              <ClipboardList v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
            </span>
            <span class="min-w-0 flex-1"><span class="block text-[13px] font-medium">/{{ command.id }}</span><span class="block truncate text-[11px] text-text-muted">{{ command.description }}</span></span>
            <ChevronRight v-if="command.id === 'skill' || command.id === 'resource'" class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
          </button>
        </template>

        <template v-else-if="view === 'skills'">
          <button v-for="(skill, index) in skills" :id="optionID(index)" :key="skill.id" type="button" role="option" tabindex="-1" :aria-selected="activeIndex === index" :aria-disabled="optionDisabled(index)" :disabled="optionDisabled(index)" class="hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-45" :class="[optionClass, activeIndex === index ? 'bg-surface-hover' : '']" @mousedown.prevent @mouseenter="hoverOption(index)" @click="chooseSkill(skill)">
            <Plug class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span class="min-w-0 flex-1"><span class="block truncate text-[13px] font-medium text-text-primary">{{ skill.name }}</span><span class="block truncate text-[11px] text-text-muted">{{ skill.description || skill.scope }}</span></span>
            <span class="font-mono text-[9px] uppercase text-text-muted">{{ selectedSkillIDs.includes(skill.id) ? 'Added' : skill.scope }}</span>
          </button>
          <p v-if="!skills.length" :class="emptyClass">No enabled skills match.</p>
        </template>

        <template v-else-if="view === 'providers'">
          <button v-for="(provider, index) in providers" :id="optionID(index)" :key="provider.name" type="button" role="option" tabindex="-1" :aria-selected="activeIndex === index" class="hover:bg-surface-hover" :class="[optionClass, activeIndex === index ? 'bg-surface-hover' : '']" @mousedown.prevent @mouseenter="hoverOption(index)" @click="chooseProvider(provider.name)">
            <span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface"><Boxes class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" /></span>
            <span class="min-w-0 flex-1"><span class="block truncate text-[13px] font-medium text-text-primary">{{ provider.displayName || provider.name }}</span><span class="block text-[11px] text-text-muted">{{ provider.resourceTypes.length }} resource {{ provider.resourceTypes.length === 1 ? 'type' : 'types' }}</span></span>
            <ChevronRight class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
          </button>
          <p v-if="!providers.length" :class="emptyClass">No Ready providers publish usable actions.</p>
        </template>

        <template v-else>
          <div v-if="resourceLoading" class="flex items-center justify-center gap-2 px-3 py-8 text-[12px] text-text-muted"><Loader2 class="h-4 w-4 k-spin" :stroke-width="1.75" aria-hidden="true" />Loading resources…</div>
          <template v-else>
            <template v-for="({ item, kind }, index) in resources" :key="agentKitResourceSelectionKey(item)">
              <div v-if="index === 0 || resources[index - 1]?.kind !== kind" class="px-2.5 pb-1 pt-2 font-mono text-[9px] font-semibold uppercase tracking-wide text-text-muted">{{ kind }}</div>
              <button :id="optionID(index)" type="button" role="option" tabindex="-1" :aria-selected="activeIndex === index" :aria-disabled="optionDisabled(index)" :disabled="optionDisabled(index)" class="hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-45" :class="[optionClass, activeIndex === index ? 'bg-surface-hover' : '']" @mousedown.prevent @mouseenter="hoverOption(index)" @click="chooseResource(item)">
                <Hash class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
                <span class="min-w-0 flex-1"><span class="block truncate font-mono text-[12px] text-text-primary">{{ item.resourceRef.name }}</span><span class="block truncate text-[10px] text-text-muted">{{ item.resourceRef.apiVersion }}</span></span>
                <span v-if="selectedResourceKeys.has(agentKitResourceSelectionKey(item))" class="font-mono text-[9px] uppercase text-text-muted">Added</span>
              </button>
            </template>
            <p v-if="!resources.length" :class="emptyClass">No accessible resources match.</p>
            <div v-for="warning in resourceWarnings" :key="warning" role="status" class="mx-2 mt-2 flex gap-2 rounded-md border border-warning/30 bg-warning-subtle px-2.5 py-2 text-[11px] text-warning"><TriangleAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />{{ warning }}</div>
          </template>
        </template>
      </div>
    </section>
    </div>
  </Teleport>
</template>
