<!-- Canonical AgentKit pane divider. Callers own resize geometry and events. -->
<script setup lang="ts">
import { GripVertical } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'

withDefaults(defineProps<{
  resizing?: boolean
  desktopOnly?: boolean
}>(), { resizing: false, desktopOnly: false })

ensureAgentUIStyles()
</script>

<template>
  <div
    class="k-ai-pane-divider"
    :class="{ 'k-ai-pane-divider--resizing': resizing, 'k-ai-pane-divider--desktop': desktopOnly }"
    role="separator"
    aria-orientation="vertical"
    aria-label="Resize conversation and workbench panes"
    tabindex="0"
    title="Resize"
  >
    <GripVertical :stroke-width="1.75" aria-hidden="true" />
  </div>
</template>
