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

<!--
Three-way theme switcher: Light / Dark / System. Rendered as a
segmented control (the macOS Settings idiom) instead of a single
cycle button, because users couldn't tell at a glance what the cycle
button would do next — "click again to find out" is bad UX for a
non-destructive preference.

Both variants are icon-only; tooltips carry the long form so the
function is discoverable on hover.

`variant`:
  - "sidebar"  — slightly larger, full sidebar width
  - "compact"  — tighter, for horizontal + floating dock action groups
-->

<script setup lang="ts">
import { useThemeStore } from '@/stores/theme'
import { Sun, Moon, Monitor } from 'lucide-vue-next'

defineProps<{ variant?: 'sidebar' | 'compact' }>()

const theme = useThemeStore()

interface Option {
  mode: 'light' | 'dark' | 'system'
  label: string
  icon: unknown
  title: string
}

const options: Option[] = [
  { mode: 'light', label: 'Light', icon: Sun, title: 'Light mode' },
  { mode: 'dark', label: 'Dark', icon: Moon, title: 'Dark mode' },
  { mode: 'system', label: 'System', icon: Monitor, title: 'Follow system' },
]
</script>

<template>
  <!-- Sidebar variant: icon-only, full width, intended to sit in the
       bottom action area of the vertical sidebar. -->
  <div
    v-if="variant !== 'compact'"
    role="group"
    aria-label="Theme"
    class="grid grid-cols-3 gap-1 rounded-xl border border-border-subtle bg-surface-overlay/40 p-1"
  >
    <button
      v-for="o in options"
      :key="o.mode"
      type="button"
      :title="o.title"
      :aria-pressed="theme.mode === o.mode"
      :aria-label="o.title"
      class="flex h-7 items-center justify-center rounded-lg transition-all duration-200"
      :class="
        theme.mode === o.mode
          ? 'bg-accent/15 text-accent shadow-sm'
          : 'text-text-muted hover:bg-surface-overlay/60 hover:text-text-secondary'
      "
      @click="theme.setMode(o.mode)"
    >
      <component :is="o.icon" class="h-3.5 w-3.5" :stroke-width="1.75" />
    </button>
  </div>

  <!-- Compact variant: three small icon buttons, side-by-side. Tooltips
       carry the long form so the function is discoverable on hover. -->
  <div
    v-else
    role="group"
    aria-label="Theme"
    class="flex items-center gap-0.5 rounded-md border border-border-subtle bg-surface-overlay/40 p-0.5"
  >
    <button
      v-for="o in options"
      :key="o.mode"
      type="button"
      :title="o.title"
      :aria-pressed="theme.mode === o.mode"
      class="flex h-5 w-6 items-center justify-center rounded text-text-muted transition-all duration-200"
      :class="
        theme.mode === o.mode
          ? 'bg-accent/20 text-accent'
          : 'hover:bg-surface-overlay/70 hover:text-text-secondary'
      "
      @click="theme.setMode(o.mode)"
    >
      <component :is="o.icon" class="h-3 w-3" :stroke-width="2" />
    </button>
  </div>
</template>
