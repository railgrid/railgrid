import { computed, type ComputedRef, type Ref } from 'vue'
import {
  productionAccessState,
  productionDeploymentDescription,
  productionDeploymentState,
  type ProductionAccessState,
  type ProductionDeploymentState,
  type PublishingStatusTone,
} from './publishingState'
import { releasePipelineView, type ReleasePipelineView } from './promotionState'
import type { ProjectPromotionReadiness, ProjectPublishing, ProjectProviderBinding } from './types'

export interface ProductionSettingsInputs {
  promotion: Readonly<Ref<ProjectPromotionReadiness | null>>
  publishing: Readonly<Ref<ProjectPublishing | null>>
  promotionLoading: Readonly<Ref<boolean>>
  promotionBusy: Readonly<Ref<boolean>>
  promotionError: Readonly<Ref<string | null>>
  releaseArtifactNeedsAttention?: Readonly<Ref<boolean>>
  productionFormValid: Readonly<Ref<boolean>>
  selectedProjectName: Readonly<Ref<string>>
}

export interface ProductionStatusPresentation {
  label: string
  tone: PublishingStatusTone
}

export interface ProductionSettingsState {
  releasePipeline: ComputedRef<ReleasePipelineView>
  canPromote: ComputedRef<boolean>
  promotionDisabledReason: ComputedRef<string>
  productionBinding: ComputedRef<ProjectProviderBinding | null>
  productionDeployment: ComputedRef<ProductionDeploymentState>
  productionAccess: ComputedRef<ProductionAccessState>
  productionURL: ComputedRef<string>
  productionPublicationReady: ComputedRef<boolean>
  productionDescription: ComputedRef<string>
  productionPublicationStatus: ComputedRef<ProductionStatusPresentation>
  productionOverview: ComputedRef<ProductionStatusPresentation>
  productionOverviewDescription: ComputedRef<string>
  productionViewerCount: ComputedRef<string>
  productionURLPlaceholder: ComputedRef<string>
  promoteButtonLabel: ComputedRef<string>
}

/**
 * Owns the read-only production/publishing presentation contract. App.vue
 * keeps orchestration and mutations, while this composable keeps lifecycle
 * labels, URLs, readiness gates, and disabled reasons consistent.
 */
export function useProductionSettings(input: ProductionSettingsInputs): ProductionSettingsState {
  const promotionBuild = computed(() => input.promotion.value?.build ?? null)
  const productionBinding = computed(() => input.promotion.value?.production ?? null)
  const productionDeployment = computed(() => productionDeploymentState(productionBinding.value))
  const productionAccess = computed(() => productionAccessState(productionBinding.value, input.publishing.value))
  const productionURL = computed(() => productionAccess.value.url)
  const productionPublicationReady = computed(() => Boolean(
    productionBinding.value &&
    productionDeployment.value.ready &&
    input.publishing.value?.published &&
    input.publishing.value.publication?.ready,
  ))

  const releasePipeline = computed(() => releasePipelineView(input.promotion.value, {
    published: input.publishing.value?.published,
    ready: input.publishing.value?.publication?.ready,
    url: productionURL.value,
  }, {
    artifactNeedsAttention: input.releaseArtifactNeedsAttention?.value ?? false,
    statusError: input.promotionError.value,
  }))
  const canPromote = computed(() => !!input.promotion.value?.promotable && input.productionFormValid.value && !input.promotionBusy.value && !input.promotionError.value)
  const promotionDisabledReason = computed(() => {
    if (input.promotionBusy.value) return 'Promotion is in progress.'
    if (input.promotionError.value) return 'Production status is unavailable. Check again before deploying.'
    if (!input.selectedProjectName.value) return 'Select a project before checking its build status.'
    if (input.promotionLoading.value && !input.promotion.value) return 'Loading production status before enabling promotion.'
    if (input.promotionError.value && !input.promotion.value) return 'Production status is unavailable. Refresh to retry.'
    if (!input.promotion.value) return 'Checking the build status before enabling promotion…'
    if (!input.productionFormValid.value) return 'Fix the highlighted production settings before deploying.'
    if (input.promotion.value.promotable) return ''
    const note = promotionBuild.value?.note?.trim()
    if (note) return note
    switch (input.promotion.value.build.status) {
      case 'incomplete':
        return 'The build is incomplete; build every component before promoting.'
      case 'none':
        return 'No component image has been built yet; commit the project and wait for its build.'
      case 'unsupported':
        return 'This project has no production-capable template.'
      default:
        return 'The build is not ready for promotion.'
    }
  })
  const productionDescription = computed(() => {
    if (productionPublicationReady.value && !productionURL.value) {
      return 'The publication is ready; the production link is still being resolved.'
    }
    return productionDeploymentDescription(productionBinding.value, input.publishing.value)
  })
  const productionPublicationStatus = computed<ProductionStatusPresentation>(() => {
    if (productionPublicationReady.value) {
      return { label: productionURL.value ? 'Live' : 'Ready', tone: 'success' }
    }
    if (input.publishing.value?.publication?.error) return { label: 'Error', tone: 'danger' }
    return { label: productionAccess.value.label, tone: productionAccess.value.tone }
  })
  const productionOverview = computed<ProductionStatusPresentation>(() => {
    if (input.promotionLoading.value && !input.promotion.value) return { label: 'Loading', tone: 'muted' }
    if (input.promotionError.value) return { label: 'Status unavailable', tone: 'warning' }
    if (!input.promotion.value) return { label: 'Awaiting status', tone: 'muted' }
    if (!productionBinding.value) return { label: 'Not deployed', tone: 'muted' }
    if (!productionDeployment.value.ready) return { label: productionDeployment.value.label, tone: productionDeployment.value.tone }
    if (!input.publishing.value) return { label: 'Checking access', tone: 'muted' }
    if (!input.publishing.value.published) return { label: 'Ready to publish', tone: 'success' }
    if (productionPublicationReady.value) return { label: productionURL.value ? 'Live' : 'Ready', tone: 'success' }
    if (input.publishing.value.publication?.error) return { label: 'Access error', tone: 'danger' }
    return { label: 'Enabling access', tone: 'warning' }
  })
  const productionOverviewDescription = computed(() => {
    if (input.promotionLoading.value && !input.promotion.value) return 'Checking the build, deployment, and release status for this project…'
    if (input.promotionError.value) return input.promotion.value
      ? 'Production status could not be refreshed. Previously loaded details may be stale.'
      : 'Production status could not be loaded. Refresh to try again.'
    if (!input.promotion.value) return 'Production status will appear here once the project responds.'
    if (!productionBinding.value) return 'No production deployment yet. Build every component before deploying this app.'
    if (!productionDeployment.value.ready) return productionDescription.value
    if (!input.publishing.value) return 'The production deployment is running. Checking external access…'
    if (!input.publishing.value.published) return 'Ready to publish. The production deployment will keep running while you turn on external access.'
    if (productionPublicationReady.value) return productionDescription.value
    if (input.publishing.value.publication?.error) {
      return `Production is running, but external access reported an error: ${input.publishing.value.publication.error}`
    }
    return productionDescription.value
  })
  const productionViewerCount = computed(() => {
    if (!input.publishing.value?.published || input.publishing.value.publication?.mode !== 'restricted') return '—'
    return String((input.publishing.value.grants ?? []).filter((grant) => !grant.revoked).length)
  })
  const productionURLPlaceholder = computed(() => {
    if (!input.publishing.value) return 'Checking external access…'
    if (productionPublicationReady.value && !productionURL.value) return 'Publication is ready; the production link is still being resolved.'
    if (productionAccess.value.label === 'Offline') return 'No production URL is active.'
    return 'Production URL will appear when external access is ready.'
  })
  const promoteButtonLabel = computed(() => {
    if (input.promotionBusy.value) return 'Deploying…'
    return productionBinding.value ? 'Redeploy to production' : 'Deploy to production'
  })

  return {
    releasePipeline,
    canPromote,
    promotionDisabledReason,
    productionBinding,
    productionDeployment,
    productionAccess,
    productionURL,
    productionPublicationReady,
    productionDescription,
    productionPublicationStatus,
    productionOverview,
    productionOverviewDescription,
    productionViewerCount,
    productionURLPlaceholder,
    promoteButtonLabel,
  }
}
