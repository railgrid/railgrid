<script setup lang="ts">
import { Check, ChevronDown, ClipboardList, SearchCheck, Sparkles } from 'lucide-vue-next'
import { computed } from 'vue'
import { useModePickerMenu } from './useModePickerMenu'

export type AssistantResponseMode = 'default' | 'plan' | 'review'

const props = defineProps<{
  mode: AssistantResponseMode
  disabled?: boolean
}>()

const emit = defineEmits<{
  selectMode: [mode: AssistantResponseMode]
}>()

const modeOptions: readonly AssistantResponseMode[] = ['default', 'plan', 'review']

const modeLabel = computed(() => {
  if (props.mode === 'plan') return 'Plan'
  if (props.mode === 'review') return 'Review'
  return 'Default'
})

const {
  root,
  triggerID,
  menuID,
  open,
  triggerRef,
  panelRef,
  panelStyle,
  activeIndex,
  openMenu,
  closeMenu,
  selectMode,
  handleTriggerKeydown,
  handleMenuKeydown,
} = useModePickerMenu<AssistantResponseMode>({
  idPrefix: 'response-mode',
  options: modeOptions,
  selectedIndex: () => Math.max(0, modeOptions.indexOf(props.mode)),
  canInteract: () => !props.disabled,
  onSelect: mode => emit('selectMode', mode),
})

const chooseMode = selectMode
</script>

<template>
  <div ref="root" class="relative min-w-0 max-w-52">
    <button
      :id="triggerID"
      ref="triggerRef"
      type="button"
      class="app-studio-touch-target inline-flex h-8 max-w-full items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-controls="menuID"
      :aria-label="`Response mode: ${modeLabel}`"
      @click="open ? closeMenu() : openMenu()"
      @keydown="handleTriggerKeydown"
    >
      <ClipboardList v-if="mode === 'plan'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <SearchCheck v-else-if="mode === 'review'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <Sparkles v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <span class="truncate">{{ modeLabel }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="2" aria-hidden="true" />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="menuID"
        ref="panelRef"
        class="k-menu max-h-[calc(100dvh-1rem)] min-w-[min(360px,calc(100vw-1rem))] max-w-[calc(100vw-1rem)] overflow-y-auto"
        :style="panelStyle"
        role="menu"
        :aria-label="`Response mode: ${modeLabel}`"
        :aria-labelledby="triggerID"
        @keydown="handleMenuKeydown"
      >
        <div class="px-1.5 py-1 text-[11px] text-text-muted">How should App Studio respond?</div>
        <button type="button" role="menuitemradio" data-mode-index="0" :aria-checked="mode === 'default'" :tabindex="activeIndex === 0 ? 0 : -1" class="app-studio-touch-target k-menu-item flex w-full items-start gap-2 text-left" @focus="activeIndex = 0" @click="chooseMode('default')">
          <Sparkles class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block text-[12px] font-medium text-text-primary">Default</span><span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Answer, inspect, or make requested changes using current evidence.</span></span>
          <Check v-if="mode === 'default'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button type="button" role="menuitemradio" data-mode-index="1" :aria-checked="mode === 'plan'" :tabindex="activeIndex === 1 ? 0 : -1" class="app-studio-touch-target k-menu-item flex w-full items-start gap-2 text-left" @focus="activeIndex = 1" @click="chooseMode('plan')">
          <ClipboardList class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block text-[12px] font-medium text-text-primary">Plan</span><span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Investigate and produce a plan without changing the project.</span></span>
          <Check v-if="mode === 'plan'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button type="button" role="menuitemradio" data-mode-index="2" :aria-checked="mode === 'review'" :tabindex="activeIndex === 2 ? 0 : -1" class="app-studio-touch-target k-menu-item flex w-full items-start gap-2 text-left" @focus="activeIndex = 2" @click="chooseMode('review')">
          <SearchCheck class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block text-[12px] font-medium text-text-primary">Review</span><span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Inspect the current workspace and report prioritized findings without changing it.</span></span>
          <Check v-if="mode === 'review'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </button>
      </div>
    </Teleport>
  </div>
</template>
