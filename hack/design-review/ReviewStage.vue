<script setup>
import { defineAsyncComponent } from 'vue'
defineProps({ stage: String, entry: Object, catalog: Object })
const core = new Set(['primitives', 'navigation', 'route-tabs', 'status', 'feedback', 'controls', 'creation'])
const resource = new Set(['table', 'resource', 'detail', 'board', 'usage', 'copy'])
const ai = new Set(['conversation', 'studio', 'models', 'evidence'])
const Core = defineAsyncComponent(() => import('./CoreStage.vue'))
const Resource = defineAsyncComponent(() => import('./ResourceStage.vue'))
const AI = defineAsyncComponent(() => import('./AIStage.vue'))
const SpacingStage = defineAsyncComponent(() => import('./SpacingStage.vue'))
const AIActionRowStage = defineAsyncComponent(() => import('./AIActionRowStage.vue'))
const ButtonStage = defineAsyncComponent(() => import('./ButtonStage.vue'))
const TextFieldStage = defineAsyncComponent(() => import('./TextFieldStage.vue'))
const SelectFieldStage = defineAsyncComponent(() => import('./SelectFieldStage.vue'))
const AppStudioClarificationStage = defineAsyncComponent(() => import('./AppStudioClarificationStage.vue'))
const LayoutSelectorStage = defineAsyncComponent(() => import('./LayoutSelectorStage.vue'))
const ChoiceStage = defineAsyncComponent(() => import('./ChoiceStage.vue'))
const ToggleStage = defineAsyncComponent(() => import('./ToggleStage.vue'))
const UploadStage = defineAsyncComponent(() => import('./UploadStage.vue'))
const Lens = defineAsyncComponent(() => import('./LensStage.vue'))
</script>
<template>
  <Core v-if="core.has(stage)" :stage="stage" />
  <Resource v-else-if="resource.has(stage)" :stage="stage" />
  <AI v-else-if="ai.has(stage)" :stage="stage" />
  <SpacingStage v-else-if="stage === 'spacing'" />
  <AIActionRowStage v-else-if="stage === 'ai-action-row'" />
  <ButtonStage v-else-if="stage === 'button'" />
  <TextFieldStage v-else-if="stage === 'text-input' || stage === 'textarea'" :multiline="stage === 'textarea'" />
  <SelectFieldStage v-else-if="stage === 'form-select'" />
  <AppStudioClarificationStage v-else-if="stage === 'studio-clarification'" />
  <LayoutSelectorStage v-else-if="stage === 'layout-selector'" />
  <ChoiceStage v-else-if="stage === 'checkbox' || stage === 'radio'" :radio="stage === 'radio'" />
  <ToggleStage v-else-if="stage === 'toggle'" />
  <UploadStage v-else-if="stage === 'file-upload'" />
  <Lens v-else :stage="stage" :entry="entry" :catalog="catalog" />
  <section v-if="entry.id === 'design.foundations.colors'" class="spec-stack" aria-labelledby="materials-heading">
    <h2 id="materials-heading">Materials and surfaces</h2>
    <Lens stage="materials" :entry="entry" :catalog="catalog" />
  </section>
  <details v-if="entry.id === 'design.foundations.recipes'" class="review-outline">
    <summary>All documented recipe selectors</summary>
    <div class="spec-scroll"><table class="spec-data"><thead><tr><th>Selector</th><th>Contract</th></tr></thead><tbody>
      <tr v-for="item in catalog.recipeRows" :key="item.label"><td><a :href="item.source.href"><code>{{ item.label }}</code></a></td><td>{{ item.description }}</td></tr>
    </tbody></table></div>
  </details>
</template>
