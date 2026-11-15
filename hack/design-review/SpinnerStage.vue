<script setup>
import { nextTick, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
const spinnerExample = ref('idle')
const spinnerAction = ref(null)
async function settleSpinner(result) {
  spinnerExample.value = result
  await nextTick()
  spinnerAction.value?.focus()
}
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="review-spinner-title">
    <div><h3 id="review-spinner-title">Spinner in an action</h3><p class="spec-note">Simulate a connection test and choose its outcome.</p></div>
    <div class="spec-row">
      <button ref="spinnerAction" type="button" class="k-btn k-btn--ghost" :aria-disabled="spinnerExample === 'busy' ? 'true' : undefined" :aria-busy="spinnerExample === 'busy' ? 'true' : undefined" @click="spinnerExample !== 'busy' && (spinnerExample = 'busy')"><Loader2 v-if="spinnerExample === 'busy'" class="k-spin" :size="16" :stroke-width="1.75" aria-hidden="true" />{{ spinnerExample === 'busy' ? 'Testing connection…' : spinnerExample === 'failed' ? 'Retry connection test' : 'Test connection' }}</button>
      <button v-if="spinnerExample === 'busy'" type="button" class="k-btn k-btn--text" @click="settleSpinner('finished')">Complete example</button>
      <button v-if="spinnerExample === 'busy'" type="button" class="k-btn k-btn--text" @click="settleSpinner('failed')">Fail example</button>
    </div>
    <p class="spec-note" role="status" aria-live="polite">{{ spinnerExample === 'busy' ? 'Testing the connection…' : spinnerExample === 'finished' ? 'Connection test completed in this example.' : spinnerExample === 'failed' ? 'Connection test failed in this example. Retry the test.' : 'Ready to test.' }}</p>
  </section>
</template>
