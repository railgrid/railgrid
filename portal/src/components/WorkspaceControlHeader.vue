<!--
Copyright 2026 The Railgrid Authors.

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
import { Building2 } from 'lucide-vue-next'
import StatusBadge from '@/portalkit/StatusBadge.vue'

defineProps<{
  workspaceName: string
  organizationName: string
  status: string
  statusTone: 'success' | 'warning' | 'danger' | 'muted'
}>()
</script>

<template>
  <section
    class="rounded-lg border border-border-default bg-surface-raised p-4 sm:p-5"
    aria-labelledby="workspace-settings-title"
  >
    <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div class="min-w-0">
        <h2 id="workspace-settings-title" class="break-words text-xl font-semibold text-text-primary sm:text-2xl">
          {{ workspaceName }}
        </h2>
        <p class="mt-1 flex min-w-0 items-center gap-1.5 text-[12px] text-text-muted">
          <Building2 class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <span class="truncate">Organization · {{ organizationName }}</span>
        </p>
      </div>

      <div class="flex flex-wrap items-center gap-2 sm:justify-end">
        <StatusBadge :status="status" :tone="statusTone" />
        <slot name="actions" />
      </div>
    </div>

    <div v-if="$slots.details" class="mt-5 border-t border-border-subtle pt-4">
      <slot name="details" />
    </div>

    <div v-if="$slots.lifecycle" class="mt-4">
      <slot name="lifecycle" />
    </div>
  </section>
</template>
