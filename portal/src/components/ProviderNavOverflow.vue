<!--
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<script setup lang="ts">
import { computed, nextTick, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Check, ChevronDown, Dot, Puzzle } from 'lucide-vue-next'
import { useAnchoredPopover } from '@/composables/useAnchoredPopover'
import {
  isActiveRoute,
  providerFamilyItems,
  type HorizontalSection,
  type NavItem,
} from '@/lib/shellNavigation'

const props = defineProps<{
  sections: HorizontalSection[]
}>()

const route = useRoute()
const panelId = useId()
const { open, triggerRef, panelRef, panelStyle, close, toggle } = useAnchoredPopover({ width: 304 })

const providerSections = computed(() => props.sections.filter((section) => section.key !== 'static'))
const activeSection = computed(() => providerSections.value.find((section) =>
  section.items.some((item) => isActiveRoute(route.path, item.to, item.exact)),
) ?? null)
const activeFamilyItems = computed(() => activeSection.value
  ? providerFamilyItems(route.path, activeSection.value.items)
  : [])
const activeFamilyKey = computed(() => activeFamilyItems.value[0]?.familyKey ?? null)
const browseSections = computed(() => providerSections.value
  .map((section) => ({
    ...section,
    items: activeFamilyKey.value
      ? section.items.filter((item) => item.familyKey !== activeFamilyKey.value)
      : section.items,
  }))
  .filter((section) => section.items.length > 0))
const browseCount = computed(() => browseSections.value.reduce((count, section) => count + section.items.length, 0))

// A route change can come from browser history, a redirect, or another shell
// control rather than one of this menu's own links. Close the teleported panel
// on every such transition so it never lingers over the new page.
watch(() => route.fullPath, () => close())

const menuItems = (): HTMLElement[] => Array.from(
  panelRef.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
)

async function focusMenuItem(index: number): Promise<void> {
  await nextTick()
  const items = menuItems()
  if (!items.length) return
  items[Math.max(0, Math.min(index, items.length - 1))]?.focus()
}

function openMenu(focusIndex?: number): void {
  if (!browseCount.value) return
  if (!open.value) toggle()
  if (focusIndex !== undefined) void focusMenuItem(focusIndex)
}

function onTriggerKeydown(event: KeyboardEvent): void {
  // Enter and Space intentionally use the button's native click activation;
  // only ArrowDown needs an explicit menu-opening convention.
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    openMenu(0)
  }
}

function onTriggerClick(): void {
  // Pointer clicks and native Enter/Space activation share this path. A
  // repeated click closes the panel and never schedules focus into the node
  // that Vue is about to destroy.
  if (open.value) close()
  else openMenu(0)
}

function onMenuKeydown(event: KeyboardEvent): void {
  const items = menuItems()
  const current = items.indexOf(document.activeElement as HTMLElement)
  if (event.key === 'Escape') {
    event.preventDefault()
    close({ restoreFocus: true })
    return
  }
  if (event.key === 'Home' || event.key === 'ArrowUp' || event.key === 'ArrowDown' || event.key === 'End') {
    event.preventDefault()
    const index = event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? items.length - 1
        : event.key === 'ArrowUp'
          ? (current <= 0 ? items.length - 1 : current - 1)
          : (current + 1) % items.length
    void focusMenuItem(index)
  }
}

function onMenuItemKeydown(event: KeyboardEvent): void {
  // Anchors do not activate on Space by default. Trigger exactly one native
  // click for the focused menuitem; Enter is left untouched so the browser's
  // native anchor activation remains the sole activation path.
  if (event.key !== ' ' && event.code !== 'Space') return
  event.preventDefault()
  const target = event.currentTarget
  if (target instanceof HTMLElement) target.click()
}

function itemLabel(item: NavItem): string {
  return item.familyLabel ?? item.label
}

function itemIsActive(item: NavItem): boolean {
  return isActiveRoute(route.path, item.to, item.exact)
}
</script>

<template>
  <!-- The current provider family stays in the primary route track. Its
       category cue keeps the parent/child relationship legible even though
       the surrounding dock is intentionally compact. -->
  <template v-if="activeFamilyItems.length && activeSection">
    <div
      class="shell-provider-family flex shrink-0 items-center gap-1 rounded-md border border-accent/25 bg-accent-subtle/30 px-1.5 py-0.5"
      :title="`${activeSection.label ?? 'Provider'} provider navigation`"
    >
      <component v-if="activeSection.icon" :is="activeSection.icon" class="h-3 w-3 shrink-0 text-accent" :stroke-width="2" aria-hidden="true" />
      <span class="text-[9px] font-semibold uppercase tracking-wider text-accent">{{ activeSection.label ?? 'Provider' }}</span>
    </div>
    <router-link
      v-for="(item, index) in activeFamilyItems"
      :key="item.key ?? item.to"
      :to="item.to"
      class="shell-provider-family-link shell-nav-link flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors duration-200"
      :class="[
        itemIsActive(item) ? 'active bg-accent-subtle text-accent shadow-[0_0_14px_var(--color-accent-glow)]' : 'text-text-secondary hover:bg-surface-overlay/40 hover:text-text-primary',
        index > 0 ? 'pl-1.5' : '',
      ]"
      :title="itemLabel(item)"
      :aria-label="itemLabel(item)"
      :aria-current="itemIsActive(item) ? 'page' : undefined"
    >
      <Dot v-if="index > 0" class="h-3.5 w-3.5 shrink-0" :stroke-width="3" aria-hidden="true" />
      <img v-else-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 shrink-0 object-contain" />
      <Puzzle v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <span>{{ itemLabel(item) }}</span>
    </router-link>
  </template>

  <div v-if="browseCount" class="relative shrink-0">
    <button
      ref="triggerRef"
      type="button"
      class="shell-provider-browse k-btn k-btn--ghost flex shrink-0 items-center gap-1.5 rounded-md border-border-subtle px-2.5 py-1 text-[11px] font-medium text-text-secondary transition-colors duration-200 hover:bg-surface-overlay/50 hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      :aria-controls="panelId"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-label="activeFamilyItems.length ? 'Browse other providers' : 'Browse providers'"
      :title="activeFamilyItems.length ? 'Browse other providers' : 'Browse providers'"
      @click="onTriggerClick"
      @keydown="onTriggerKeydown"
    >
      <Puzzle class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
      <span>{{ activeFamilyItems.length ? 'More' : 'Browse' }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition-transform duration-200" :class="open ? 'rotate-180' : ''" :stroke-width="1.75" aria-hidden="true" />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="panelId"
        ref="panelRef"
        class="provider-nav-overflow-menu k-menu fixed z-[80] max-h-[min(28rem,calc(100vh-16px))] w-[304px] max-w-[calc(100vw-16px)] overflow-y-auto"
        role="menu"
        :aria-label="activeFamilyItems.length ? 'Other providers' : 'Providers'"
        :style="panelStyle"
        @keydown="onMenuKeydown"
      >
        <template v-for="(section, sectionIndex) in browseSections" :key="section.key">
          <div
            class="flex items-center gap-1.5 px-2 pb-1 pt-2"
            role="presentation"
          >
            <component v-if="section.icon" :is="section.icon" class="h-3 w-3 shrink-0 text-text-secondary" :stroke-width="2" aria-hidden="true" />
            <span class="text-[10px] font-semibold uppercase tracking-wider text-text-secondary">{{ section.label ?? 'Other' }}</span>
          </div>
          <router-link
            v-for="item in section.items"
            :key="item.key ?? item.to"
            :to="item.to"
            role="menuitem"
            class="provider-nav-overflow-item k-menu-item"
            :class="itemIsActive(item) ? 'is-selected' : ''"
            :aria-current="itemIsActive(item) ? 'page' : undefined"
            @click="close()"
            @keydown="onMenuItemKeydown"
          >
            <Dot v-if="item.parentTo" class="ml-1 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="3" aria-hidden="true" />
            <img v-else-if="item.iconURL" :src="item.iconURL" alt="" class="h-3.5 w-3.5 shrink-0 object-contain" />
            <Puzzle v-else class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate">{{ item.label }}</span>
            <Check v-if="itemIsActive(item)" class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="2" aria-hidden="true" />
          </router-link>
          <div v-if="sectionIndex < browseSections.length - 1" class="k-menu-sep" />
        </template>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
@media (pointer: coarse) {
  .shell-provider-family-link,
  .shell-provider-browse,
  .provider-nav-overflow-item {
    min-height: 44px;
    min-width: 44px;
  }
}
</style>
