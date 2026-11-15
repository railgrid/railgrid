<script setup lang="ts">
import { computed, ref } from 'vue'
import MeteringUsageList from '@/components/MeteringUsageList.vue'

const total = 8
const uploaded = ref(3)
const complete = computed(() => uploaded.value === total)
const value = computed(() => `${uploaded.value} of ${total} files`)
const limits = [
  { meter: 'edges', shape: 'Level', enforce: 'Hard', max: '10' },
  { meter: 'workspaces', shape: 'Level', enforce: 'Hard', max: '4' },
  { meter: 'repositories', shape: 'Level', enforce: 'Soft', max: '5' },
]
const usage = { edges: 9, workspaces: 4, repositories: 7 }

function advance() {
  uploaded.value = complete.value ? 3 : uploaded.value + 1
}
</script>

<template>
  <div class="spec-stack">
    <section class="spec-section spec-stack" aria-labelledby="progress-task">
      <h3 id="progress-task">Project file upload</h3>
      <div v-if="!complete" class="k-task-progress">
        <div class="k-task-progress__header">
          <label class="k-task-progress__label" for="review-task-progress">Uploading project files</label>
          <span class="k-task-progress__value">{{ value }}</span>
        </div>
        <progress id="review-task-progress" class="k-progress-native" :value="uploaded" :max="total" :aria-valuetext="`${value} uploaded`" />
      </div>
      <p class="spec-note" role="status">{{ complete ? 'All 8 files uploaded.' : '' }}</p>
      <button class="k-btn k-btn--ghost spec-fit" type="button" @click="advance">{{ complete ? 'Restart example' : 'Finish next file' }}</button>
    </section>
    <section class="spec-section spec-stack" aria-labelledby="progress-quota">
      <h3 id="progress-quota">Plan usage</h3>
      <MeteringUsageList variant="compact" :limits="limits" :usage="usage" />
      <a class="review-source-link" href="?entry=design.components.dashboard-usage">Open dashboard usage review</a>
    </section>
  </div>
</template>
