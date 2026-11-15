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
defineProps<{ completed: number; total: number; succeeded: number; failed: number }>()
</script>

<template>
  <div class="space-y-2 rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2.5">
    <div class="k-task-progress">
      <div class="k-task-progress__header">
        <span class="k-task-progress__label">Submitting workspace deletion requests</span>
        <span class="k-task-progress__value">{{ completed }} of {{ total }} requests finished</span>
      </div>
      <progress class="k-progress-native" :value="completed" :max="total" aria-label="Workspace deletion requests finished" :aria-valuetext="`${completed} of ${total} requests finished`" />
    </div>
    <p class="text-[11px] text-text-muted">{{ succeeded }} requested<span v-if="failed"> · {{ failed }} failed</span>.</p>
  </div>
</template>
