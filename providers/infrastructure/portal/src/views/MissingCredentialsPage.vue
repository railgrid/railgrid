<script setup lang="ts">
import { ArrowLeft } from 'lucide-vue-next'

const emit = defineEmits<{ (e: 'navigate', view: string): void }>()
</script>

<template>
  <section class="page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" @click="emit('navigate', 'catalog')"><ArrowLeft :size="14" aria-hidden="true" /> Back to templates</button>
    <header class="page-head">
      <div>
        <h2 class="page-title">Check provisioning</h2>
        <p class="page-meta">This older credentials page is no longer used for provisioning.</p>
      </div>
    </header>
    <section class="panel k-card">
      <h3 class="panel-title">Review an instance</h3>
      <p>Open Instances and select your instance to check its status and conditions. If a referenced Secret needs attention, the conditions provide details.</p>
      <button type="button" class="k-btn k-btn--primary" @click="emit('navigate', 'instances')">View instances</button>
    </section>
  </section>
</template>
