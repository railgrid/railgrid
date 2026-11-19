<script setup lang="ts">
// Renders one resolved view value with a type-appropriate presentation:
//   text  → plain span
//   link  → clickable anchor (new tab) with an external-link glyph
//   code  → selectable monospace text with an adjacent copy button
//   badge → muted label
// Driven entirely by a ResolvedValue from view.ts, so list cells and detail
// fields render identically.
import { ExternalLink } from 'lucide-vue-next'
import CopyValue from '../portalkit/CopyValue.vue'
import type { ResolvedValue } from '../view'

withDefaults(defineProps<{ value: ResolvedValue; interactive?: boolean }>(), {
  interactive: true,
})
</script>

<template>
  <span v-if="value.empty" class="text-[12px] text-text-muted/50">—</span>

  <span v-else-if="!interactive" class="text-[12px] text-text-secondary">{{ value.text }}</span>

  <a
    v-else-if="value.type === 'link' && value.href"
    :href="value.href"
    target="_blank"
    rel="noopener noreferrer"
    class="view-value-link inline-flex items-center gap-1 text-[12px] font-medium text-accent transition-colors hover:underline"
    @click.stop
  >
    <span class="view-value-text">{{ value.text }}</span>
    <ExternalLink :size="12" :stroke-width="1.75" class="opacity-70" aria-hidden="true" />
  </a>

  <CopyValue v-else-if="value.type === 'code'" :value="value.text" :label="`Copy ${value.text}`" tooltip="Copy value" />

  <span
    v-else-if="value.type === 'badge'"
    class="k-badge k-badge--muted k-badge--label"
  >
    {{ value.text }}
  </span>

  <span v-else class="text-[12px] text-text-secondary">{{ value.text }}</span>
</template>
