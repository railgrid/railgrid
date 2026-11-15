<script setup>
import { nextTick, ref } from 'vue'
import InlineNotification from '../../provider-sdk/portalkit-vue/InlineNotification.vue'
import ModelActivationFeedback from '../../providers/app-studio/portal/src/ModelActivationFeedback.vue'

const visible = ref(true)
const busy = ref(false)
const completed = ref(false)
const notice = ref(null)
const resetButton = ref(null)
const tipVisible = ref(true)
const tipNotice = ref(null)
const tipReset = ref(null)
const activation = ref('waiting')
const activationError = ref(null)
function retry() { busy.value = true }
async function dismissTip() {
  tipVisible.value = false
  await nextTick()
  tipReset.value?.focus()
}
async function resetTip() {
  tipVisible.value = true
  await nextTick()
  tipNotice.value?.$el?.querySelector('.k-inline-notification__dismiss')?.focus()
}
async function finish() {
  busy.value = false; completed.value = true; visible.value = false
  await nextTick()
  resetButton.value?.focus()
}
async function reset() {
  busy.value = false; completed.value = false; visible.value = true
  await nextTick()
  notice.value?.$el?.querySelector('.k-inline-notification__action')?.focus()
}
</script>

<template>
  <div class="spec-stack">
    <section class="spec-section spec-stack" aria-labelledby="notice-tones">
      <h3 id="notice-tones">Tones</h3>
      <p class="spec-note">Anatomy: a decorative tone icon, message, optional action, and optional dismiss control. Static examples have announcements turned off.</p>
      <InlineNotification v-if="tipVisible" ref="tipNotice" tone="info" message="Changes will apply to new runs." announce="off" dismissible @dismiss="dismissTip" />
      <button v-else ref="tipReset" class="k-btn k-btn--text" type="button" @click="resetTip">Show optional tip</button>
      <InlineNotification tone="success" message="Connection verified." announce="off" />
      <InlineNotification tone="warning" message="The existing model keeps its previous connection until the revision is ready." announce="off" />
    </section>
    <section class="spec-section spec-stack" aria-labelledby="notice-recovery">
      <h3 id="notice-recovery">Failure and recovery</h3>
      <p class="spec-note">Retry starts a simulated request. Complete it to see the result; no service is contacted.</p>
      <InlineNotification v-if="visible" ref="notice" tone="error" title="Could not load models" message="Check the provider connection, then try again." action-label="Retry" action-busy-label="Retrying models…" :action-busy="busy" @action="retry" />
      <InlineNotification v-else-if="completed" tone="success" message="Models loaded in the fixture." />
      <div class="spec-row"><button v-if="busy" class="k-btn k-btn--ghost" type="button" @click="finish">Complete simulated retry</button><button v-if="!visible" ref="resetButton" class="k-btn k-btn--text" type="button" @click="reset">Reset example</button></div>
      <div class="spec-row"><a class="k-btn k-btn--ghost" href="http://127.0.0.1:5198/usage-recovery.html" target="_blank" rel="noopener">Open Agents usage recovery fixture</a><a class="k-btn k-btn--ghost" href="http://127.0.0.1:5198/?provider=app&amp;scenario=app-model-retry&amp;theme=dark&amp;route=~new" target="_blank" rel="noopener">Open App Studio model retry fixture</a></div>
    </section>
    <section class="spec-section spec-stack" aria-labelledby="notice-activation">
      <h3 id="notice-activation">Saved model readiness</h3>
      <p class="spec-note">This mounts App Studio's feedback with sample state. Check again keeps the action in place while the simulated read is pending.</p>
      <ModelActivationFeedback :loading="activation === 'loading' || activation === 'retrying'" :retrying="activation === 'retrying'" :error="activationError" @check="activation = 'retrying'" />
      <div class="spec-row"><button class="k-btn k-btn--ghost" type="button" @click="activationError = null; activation = 'loading'">Show initial check</button><button class="k-btn k-btn--ghost" type="button" @click="activationError = null; activation = 'waiting'">Show still pending</button><button class="k-btn k-btn--ghost" type="button" @click="activationError = 'Could not check model readiness.'; activation = 'waiting'">Simulate failure</button></div>
    </section>
  </div>
</template>
