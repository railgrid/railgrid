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
import { ArrowRight, Building2, CheckCircle2, FolderTree } from 'lucide-vue-next'
import StatusBadge from '@/portalkit/StatusBadge.vue'

defineProps<{
  workspaceName: string
  organizationName: string
  status: string
  statusTone: 'success' | 'warning' | 'danger' | 'muted'
  activeWorkspaceName: string | null
  isActive: boolean
  switchDisabled: boolean
  switchDisabledReason?: string | null
}>()

defineEmits<{
  activate: []
}>()
</script>

<template>
  <section
    class="rounded-lg border border-border-default bg-surface-raised p-4 sm:p-5"
    aria-labelledby="inspected-workspace-title"
  >
    <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div class="min-w-0">
        <div class="mb-2 flex flex-wrap items-center gap-2">
          <span class="k-badge k-badge--muted">
            <FolderTree class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
            Inspecting workspace
          </span>
          <StatusBadge :status="status" :tone="statusTone" />
        </div>
        <h2 id="inspected-workspace-title" class="break-words text-xl font-semibold text-text-primary sm:text-2xl">
          {{ workspaceName }}
        </h2>
        <p class="mt-1 flex min-w-0 items-center gap-1.5 text-[12px] text-text-muted">
          <Building2 class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <span class="truncate">Organization · {{ organizationName }}</span>
        </p>
      </div>

      <div class="flex flex-wrap items-center gap-2 sm:justify-end">
        <slot name="actions" />
      </div>
    </div>

    <div v-if="$slots.details" class="mt-5 border-t border-border-subtle pt-4">
      <slot name="details" />
    </div>

    <div
      class="mt-5 flex flex-col gap-3 border-t border-border-subtle pt-4 sm:flex-row sm:items-center sm:justify-between"
    >
      <div class="flex min-w-0 items-start gap-2.5">
        <CheckCircle2
          v-if="isActive"
          class="mt-0.5 h-4 w-4 shrink-0 text-success"
          :stroke-width="1.75"
          aria-hidden="true"
        />
        <ArrowRight
          v-else
          class="mt-0.5 h-4 w-4 shrink-0 text-warning"
          :stroke-width="1.75"
          aria-hidden="true"
        />
        <div class="min-w-0" role="status" aria-live="polite" aria-atomic="true">
          <p class="text-[12px] font-medium text-text-primary">
            <template v-if="isActive">This is your active operating Workspace.</template>
            <template v-else>
              Active operating Workspace:
              <span class="font-mono text-text-secondary">{{ activeWorkspaceName || 'None selected' }}</span>
            </template>
          </p>
          <p class="mt-0.5 max-w-2xl text-[11px] leading-relaxed text-text-muted">
            <template v-if="isActive">
              Resources, tools, and agents outside Settings use this Workspace context.
            </template>
            <template v-else-if="switchDisabledReason">
              {{ switchDisabledReason }}
            </template>
            <template v-else>
              Changes below affect the inspected Workspace only. Switch context before using its resources, tools, or agents elsewhere.
            </template>
          </p>
        </div>
      </div>

      <button
        v-if="!isActive"
        type="button"
        class="k-btn k-btn--primary min-h-11 shrink-0 justify-center px-3 text-[12px] sm:min-h-0 sm:justify-start sm:py-1.5"
        :disabled="switchDisabled"
        :title="switchDisabledReason || 'Make this the active operating Workspace and open its dashboard'"
        @click="$emit('activate')"
      >
        <ArrowRight class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        Switch operating context
      </button>
    </div>

    <div v-if="$slots.lifecycle" class="mt-4">
      <slot name="lifecycle" />
    </div>
  </section>
</template>
