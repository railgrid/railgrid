<script setup lang="ts">
import { Brain, CornerDownRight, Eye, Link2, Wrench } from 'lucide-vue-next'
import type { Agent, ModelInfo } from '../types'

defineProps<{
  model?: ModelInfo
  catalogFallback: string
  primaryAgents: Agent[]
  fallbackAgents: Agent[]
}>()

function formatContext(value: number): string {
  if (value >= 1e6) return `${value / 1e6}M ctx`
  if (value >= 1e3) return `${Math.round(value / 1e3)}k ctx`
  return `${value} ctx`
}
</script>

<template>
  <template v-if="model">
    <div class="agents-model-chips">
      <span v-if="model.contextWindow" class="agents-chip">{{ formatContext(model.contextWindow) }}</span>
      <span v-if="model.vision" class="agents-chip"><Eye :stroke-width="1.75" aria-hidden="true" /> vision</span>
      <span v-if="model.toolCall" class="agents-chip"><Wrench :stroke-width="1.75" aria-hidden="true" /> tools</span>
      <span v-if="model.reasoning" class="agents-chip"><Brain :stroke-width="1.75" aria-hidden="true" /> reasoning</span>
    </div>
    <p class="agents-hint">
      ${{ model.inputPer1M }} input · ${{ model.outputPer1M }} output<br>
      USD per 1M tokens · catalog estimate
    </p>
  </template>
  <p v-else class="agents-hint">{{ catalogFallback }}</p>
  <div class="agents-model-assign">
    <span v-for="agent in primaryAgents" :key="`p-${agent.metadata.name}`" class="agents-chip agents-chip-primary">
      <Link2 :stroke-width="1.75" aria-hidden="true" /> Primary: {{ agent.spec?.displayName || agent.metadata.name }}
    </span>
    <span v-for="agent in fallbackAgents" :key="`f-${agent.metadata.name}`" class="agents-chip agents-chip-fallback">
      <CornerDownRight :stroke-width="1.75" aria-hidden="true" /> Fallback: {{ agent.spec?.displayName || agent.metadata.name }}
    </span>
    <span v-if="!primaryAgents.length && !fallbackAgents.length" class="muted agents-assign-none">Not assigned to any agent</span>
  </div>
</template>
