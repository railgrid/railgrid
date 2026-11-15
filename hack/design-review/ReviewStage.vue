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
const CopyProductStage = defineAsyncComponent(() => import('./CopyProductStage.vue'))
const UploadStage = defineAsyncComponent(() => import('./UploadStage.vue'))
const FieldGroupStage = defineAsyncComponent(() => import('./FieldGroupStage.vue'))
const SearchStage = defineAsyncComponent(() => import('./SearchStage.vue'))
const ConfirmReviewStage = defineAsyncComponent(() => import('./ConfirmReviewStage.vue'))
const ResourcePageStage = defineAsyncComponent(() => import('./ResourcePageStage.vue'))
const EmptyStatesStage = defineAsyncComponent(() => import('./EmptyStatesStage.vue'))
const TooltipReviewStage = defineAsyncComponent(() => import('./TooltipReviewStage.vue'))
const ProgressReviewStage = defineAsyncComponent(() => import('./ProgressReviewStage.vue'))
const TimestampStage = defineAsyncComponent(() => import('./TimestampStage.vue'))
const DisclosureReviewStage = defineAsyncComponent(() => import('./DisclosureReviewStage.vue'))
const ConditionsStage = defineAsyncComponent(() => import('./ConditionsStage.vue'))
const BackLinkStage = defineAsyncComponent(() => import('./BackLinkStage.vue'))
const SectionCardStage = defineAsyncComponent(() => import('./SectionCardStage.vue'))
const StatCardsStage = defineAsyncComponent(() => import('./StatCardsStage.vue'))
const InlineNotificationStage = defineAsyncComponent(() => import('./InlineNotificationStage.vue'))
const AIExecutionStage = defineAsyncComponent(() => import('./AIExecutionStage.vue'))
const SkillCatalogStage = defineAsyncComponent(() => import('./SkillCatalogStage.vue'))
const ModelReasoningStage = defineAsyncComponent(() => import('./ModelReasoningStage.vue'))
const TokenizedListStage = defineAsyncComponent(() => import('./TokenizedListStage.vue'))
const TerminalStage = defineAsyncComponent(() => import('./TerminalStage.vue'))
const DigestScheduleStage = defineAsyncComponent(() => import('./DigestScheduleStage.vue'))
const LinkStage = defineAsyncComponent(() => import('./LinkStage.vue'))
const DashboardTileStage = defineAsyncComponent(() => import('./DashboardTileStage.vue'))
const LayeringStage = defineAsyncComponent(() => import('./LayeringStage.vue'))
const EdgesDashboardTileStage = defineAsyncComponent(() => import('./EdgesDashboardTileStage.vue'))
const CodeDashboardTileStage = defineAsyncComponent(() => import('./CodeDashboardTileStage.vue'))
const InfrastructureDashboardTileStage = defineAsyncComponent(() => import('./InfrastructureDashboardTileStage.vue'))
const AppStudioDashboardTileStage = defineAsyncComponent(() => import('./AppStudioDashboardTileStage.vue'))
const PlainDashboardTileStage = defineAsyncComponent(() => import('./PlainDashboardTileStage.vue'))
const SpinnerStage = defineAsyncComponent(() => import('./SpinnerStage.vue'))
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
  <CopyProductStage v-else-if="stage === 'copy-control'" />
  <UploadStage v-else-if="stage === 'file-upload'" />
  <FieldGroupStage v-else-if="stage === 'field-group'" />
  <SearchStage v-else-if="stage === 'search-input'" />
  <ConfirmReviewStage v-else-if="stage === 'confirm-review'" />
  <ResourcePageStage v-else-if="stage === 'resource-page-loading'" :entry="entry" />
  <EmptyStatesStage v-else-if="stage === 'first-run-guide'" />
  <TooltipReviewStage v-else-if="stage === 'tooltip'" />
  <ProgressReviewStage v-else-if="stage === 'progress'" />
  <TimestampStage v-else-if="stage === 'timestamp'" />
  <DisclosureReviewStage v-else-if="stage === 'disclosure'" />
  <a v-else-if="stage === 'product'" class="review-source-link" :href="entry.review.product.href">{{ entry.review.product.label }}</a>
  <ConditionsStage v-else-if="stage === 'conditions'" />
  <BackLinkStage v-else-if="stage === 'back-link'" />
  <SectionCardStage v-else-if="stage === 'section-card'" />
  <StatCardsStage v-else-if="stage === 'stat-cards'" />
  <InlineNotificationStage v-else-if="stage === 'inline-notification'" />
  <AIExecutionStage v-else-if="stage === 'ai-execution'" />
  <SkillCatalogStage v-else-if="stage === 'skill-catalog'" />
  <ModelReasoningStage v-else-if="stage === 'model-reasoning'" />
  <TokenizedListStage v-else-if="stage === 'tokenized-list'" />
  <TerminalStage v-else-if="stage === 'terminal-dock'" />
  <DigestScheduleStage v-else-if="stage === 'digest-schedule'" />
  <LinkStage v-else-if="stage === 'link'" />
  <DashboardTileStage v-else-if="stage === 'dashboard-tile'" />
  <LayeringStage v-else-if="stage === 'layering'" />
  <SpinnerStage v-else-if="stage === 'spinner'" />
  <Lens v-else :stage="stage" :entry="entry" :catalog="catalog" />
  <section v-if="entry.id === 'design.foundations.colors'" class="spec-stack" aria-labelledby="materials-heading">
    <h2 id="materials-heading">Materials and surfaces</h2>
    <Lens stage="materials" :entry="entry" :catalog="catalog" />
  </section>
  <EdgesDashboardTileStage v-if="entry.id === 'design.components.dashboard-tile'" />
  <CodeDashboardTileStage v-if="entry.id === 'design.components.dashboard-tile'" />
  <InfrastructureDashboardTileStage v-if="entry.id === 'design.components.dashboard-tile'" />
  <AppStudioDashboardTileStage v-if="entry.id === 'design.components.dashboard-tile'" />
  <PlainDashboardTileStage v-if="entry.id === 'design.components.dashboard-tile'" />
  <details v-if="entry.id === 'design.foundations.recipes'" class="review-outline">
    <summary>All documented recipe selectors</summary>
    <div class="spec-scroll"><table class="spec-data"><thead><tr><th>Selector</th><th>Contract</th></tr></thead><tbody>
      <tr v-for="item in catalog.recipeRows" :key="item.label"><td><a :href="item.source.href"><code>{{ item.label }}</code></a></td><td>{{ item.description }}</td></tr>
    </tbody></table></div>
  </details>
</template>
