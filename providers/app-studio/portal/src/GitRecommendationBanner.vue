<script setup lang="ts">
import { portalHref } from './portalkit/navigation'
import { computed } from 'vue'
import { ExternalLink, GitBranch, Loader2, RefreshCw } from 'lucide-vue-next'
import type { ProjectCreateReadiness } from './createReadiness'

const props = defineProps<{ readiness: ProjectCreateReadiness | null; checking: boolean; error?: string; connectionUrl?: string; catalogUrl?: string }>()
const connectionUrl = computed(() => props.connectionUrl ?? portalHref('/ui/providers/code/connections'))
const catalogUrl = computed(() => props.catalogUrl ?? portalHref('/providers'))
defineEmits<{ retry: [] }>()
const missingProvider = computed(() => props.readiness?.gitConnection.status === 'provider-missing')
const failed = computed(() => !!props.error || props.readiness?.gitConnection.status === 'failed')
const validating = computed(() => props.readiness?.gitConnection.status === 'validating')
</script>

<template>
  <aside class="k-inline-notification" aria-label="Git recommendation">
    <GitBranch class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
    <div class="min-w-0 flex-1 sm:flex sm:items-center sm:justify-between sm:gap-4">
      <div class="min-w-0">
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h3 class="text-[13px] font-semibold text-text-primary">Keep your code backed up with Git</h3>
        <span class="text-[11px] text-text-muted">Recommended</span>
      </div>
      <p class="mt-1 text-[12px] leading-5 text-text-secondary">Save an external copy of your source and unlock repository history. Connect now or keep building.</p>
      <p v-if="failed && !checking" class="mt-1 text-[12px] text-text-secondary" role="status">{{ readiness?.gitConnection.message || 'Git could not be verified. You can retry or continue without it.' }}</p>
      <p v-else-if="validating" class="mt-1 text-[12px] text-text-secondary" role="status">Git is still validating. You can continue without it.</p>
      </div>
      <div class="mt-3 flex shrink-0 flex-wrap items-center gap-2 sm:mt-0">
        <a :href="missingProvider ? catalogUrl : connectionUrl" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--ghost no-underline">
          {{ missingProvider ? 'Enable Code provider' : failed ? 'Fix Git connection' : validating ? 'View Git connection' : 'Connect Git' }} <ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />
        </a>
        <button type="button" class="k-btn k-btn--text" :disabled="checking" :aria-busy="checking" :aria-label="checking ? 'Checking Git connection' : 'Check Git connection'" title="Check Git connection" @click="$emit('retry')">
          <Loader2 v-if="checking" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
          <RefreshCw v-else class="h-3.5 w-3.5" aria-hidden="true" />
        </button>
        <slot />
      </div>
    </div>
  </aside>
</template>
