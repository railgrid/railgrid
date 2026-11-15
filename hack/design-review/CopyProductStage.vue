<script setup>
import { ref } from 'vue'
import ViewValue from '../../providers/infrastructure/portal/src/components/ViewValue.vue'
import SecretHandoff from '../../providers/agents/portal/src/components/SecretHandoff.vue'
import TechnicalDetails from '../../providers/agents/portal/src/components/TechnicalDetails.vue'
import '../../providers/infrastructure/portal/src/style.css'
import '../../providers/agents/portal/src/style.css'

const value = { type: 'code', text: 'db.internal.example:5432', empty: false }
const handoffVisible = ref(true)
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="copy-product-title">
    <div>
      <h3 id="copy-product-title">Infrastructure value</h3>
      <p class="spec-note">Anatomy: a visible value, its named copy action, and local feedback. Clipboard success or failure depends on browser permissions; no backend is contacted.</p>
    </div>
    <railgrid-provider-infrastructure><dl><dt>Endpoint</dt><dd><ViewValue :value="value" /></dd></dl></railgrid-provider-infrastructure>
  </section>
  <section class="spec-section spec-stack" aria-labelledby="copy-diagnostic-title">
    <div>
      <h3 id="copy-diagnostic-title">Run failure details</h3>
      <p class="spec-note">The Agents disclosure copies a synthetic diagnostic. Open it to review the local confirmation and manual-copy recovery.</p>
    </div>
    <railgrid-provider-agents><TechnicalDetails diagnostic="Runner exited before the tool returned a result." compact copyable /></railgrid-provider-agents>
  </section>
  <section class="spec-section spec-stack" aria-labelledby="copy-handoff-title">
    <div>
      <h3 id="copy-handoff-title">Masked setup handoff</h3>
      <p class="spec-note">The Agents component keeps a one-time value masked. This example copies a synthetic value; clipboard availability depends on the browser.</p>
    </div>
    <railgrid-provider-agents><SecretHandoff v-if="handoffVisible" value="sample-setup-value" label="Slack request URL" copy-label="Copy Slack request URL" @cleared="handoffVisible = false" /></railgrid-provider-agents>
    <button v-if="!handoffVisible" class="k-btn k-btn--ghost" type="button" @click="handoffVisible = true">Restore example</button>
  </section>
</template>
