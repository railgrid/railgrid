<script setup lang="ts">
import { Check, ChevronDown, Hand, ShieldCheck, ShieldX } from 'lucide-vue-next'
import type { ProjectAssistantApprovalMode } from './types'
import { useModePickerMenu } from './useModePickerMenu'

const props = defineProps<{
  mode: ProjectAssistantApprovalMode
  disabled?: boolean
  busy?: boolean
}>()

const emit = defineEmits<{
  select: [mode: ProjectAssistantApprovalMode]
}>()

const modeOptions: readonly ProjectAssistantApprovalMode[] = ['on_request', 'always_ask', 'never']
const labels: Record<ProjectAssistantApprovalMode, string> = {
  on_request: 'Ask when needed',
  always_ask: 'Always ask',
  never: 'Never allow',
}

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
} = useModePickerMenu<ProjectAssistantApprovalMode>({
  idPrefix: 'approval-mode',
  options: modeOptions,
  selectedIndex: () => Math.max(0, modeOptions.indexOf(props.mode)),
  canInteract: () => !props.disabled && !props.busy,
  onSelect: mode => {
    if (mode !== props.mode) emit('select', mode)
  },
})

const choose = selectMode
</script>

<template>
  <div ref="root" class="relative min-w-0 max-w-44">
    <button
      :id="triggerID"
      ref="triggerRef"
      type="button"
      class="app-studio-touch-target inline-flex h-8 max-w-full items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled || busy"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-controls="menuID"
      :aria-label="`Approval mode: ${labels[mode]}`"
      @click="open ? closeMenu() : openMenu()"
      @keydown="handleTriggerKeydown"
    >
      <ShieldCheck v-if="mode === 'on_request'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <Hand v-else-if="mode === 'always_ask'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <ShieldX v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <span class="truncate">{{ busy ? 'Saving…' : labels[mode] }}</span>
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
        :aria-label="`Approval mode: ${labels[mode]}`"
        :aria-labelledby="triggerID"
        @keydown="handleMenuKeydown"
      >
        <div class="flex items-center justify-between gap-2 px-1.5 py-1"><span class="text-[11px] text-text-muted">How should App Studio actions be approved?</span></div>
        <button type="button" role="menuitemradio" :aria-checked="mode === 'on_request'" :tabindex="activeIndex === 0 ? 0 : -1" class="app-studio-touch-target k-menu-item flex w-full items-start gap-2 text-left" @focus="activeIndex = 0" @click="choose('on_request')">
          <ShieldCheck class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block text-[12px] font-medium text-text-primary">Ask when needed</span><span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Run routine workspace, build, test, and lint actions automatically. Ask before consequential external effects.</span></span>
          <Check v-if="mode === 'on_request'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button type="button" role="menuitemradio" :aria-checked="mode === 'always_ask'" :tabindex="activeIndex === 1 ? 0 : -1" class="app-studio-touch-target k-menu-item flex w-full items-start gap-2 text-left" @focus="activeIndex = 1" @click="choose('always_ask')">
          <Hand class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block text-[12px] font-medium text-text-primary">Always ask</span><span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Ask before actions that change state or invoke external operations.</span></span>
          <Check v-if="mode === 'always_ask'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button type="button" role="menuitemradio" :aria-checked="mode === 'never'" :tabindex="activeIndex === 2 ? 0 : -1" class="app-studio-touch-target k-menu-item flex w-full items-start gap-2 text-left" @focus="activeIndex = 2" @click="choose('never')">
          <ShieldX class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block text-[12px] font-medium text-text-primary">Never allow</span><span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Keep the assistant read-only and reject actions requiring approval.</span></span>
          <Check v-if="mode === 'never'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </button>
      </div>
    </Teleport>
  </div>
</template>
