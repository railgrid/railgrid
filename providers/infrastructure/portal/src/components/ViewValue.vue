<script setup lang="ts">
// Renders one resolved view value with a type-appropriate presentation:
//   text  → plain span
//   link  → clickable anchor (new tab) with an external-link glyph
//   code  → monospace pill with a copy button
//   badge → neutral pill
// Driven entirely by a ResolvedValue from view.ts, so list cells and detail
// fields render identically.
import { ref } from 'vue'
import type { ResolvedValue } from '../view'

const props = defineProps<{ value: ResolvedValue }>()

const copied = ref(false)
async function copy() {
  try {
    await navigator.clipboard.writeText(props.value.text)
    copied.value = true
    window.setTimeout(() => (copied.value = false), 1200)
  } catch {
    // Clipboard blocked (insecure context / permissions) — no-op; the value
    // is still visible and selectable.
  }
}
</script>

<template>
  <span v-if="value.empty" class="text-[12px] text-text-muted/50">—</span>

  <a
    v-else-if="value.type === 'link' && value.href"
    :href="value.href"
    target="_blank"
    rel="noopener noreferrer"
    class="inline-flex items-center gap-1 text-[12px] font-medium text-accent transition-colors hover:underline"
    @click.stop
  >
    {{ value.text }}
    <svg viewBox="0 0 24 24" class="h-3 w-3 opacity-70" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" /><polyline points="15 3 21 3 21 9" /><line x1="10" y1="14" x2="21" y2="3" />
    </svg>
  </a>

  <span
    v-else-if="value.type === 'code'"
    class="inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-text-secondary"
  >
    {{ value.text }}
    <button
      class="text-text-muted/60 transition-colors hover:text-text-primary"
      :title="copied ? 'Copied' : 'Copy'"
      @click.stop="copy"
    >
      <svg v-if="!copied" viewBox="0 0 24 24" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <rect x="9" y="9" width="13" height="13" rx="2" ry="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
      </svg>
      <svg v-else viewBox="0 0 24 24" class="h-3 w-3 text-success" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
        <polyline points="20 6 9 17 4 12" />
      </svg>
    </button>
  </span>

  <span
    v-else-if="value.type === 'badge'"
    class="inline-flex items-center rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 text-[11px] font-medium text-text-secondary"
  >
    {{ value.text }}
  </span>

  <span v-else class="text-[12px] text-text-secondary">{{ value.text }}</span>
</template>
