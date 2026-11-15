<script setup>
import { ChevronDown } from 'lucide-vue-next'
import AIActivityDisclosureStage from './AIActivityDisclosureStage.vue'
import TechnicalDetails from '../../providers/agents/portal/src/components/TechnicalDetails.vue'
import '../../providers/agents/portal/src/style.css'
</script>

<template>
  <div class="spec-stack">
    <section class="spec-section spec-stack" aria-labelledby="run-disclosures">
      <h3 id="run-disclosures">Run diagnostics</h3>
      <p class="spec-note" role="status">The production health check timed out. Review the step before continuing the rollout.</p>
      <details class="k-resource-technical">
        <summary class="k-resource-technical__summary">
          <span class="k-resource-technical__summary-copy">
            <span class="k-resource-technical__summary-label">Arguments and result</span>
            <span class="k-resource-technical__summary-context">Tool inputs and response</span>
          </span>
          <ChevronDown class="k-resource-technical__chevron" :size="16" :stroke-width="1.75" aria-hidden="true" />
        </summary>
        <div class="k-resource-technical__body">
          <pre class="k-resource-technical__pre">{"service":"api","timeoutSeconds":10}</pre>
        </div>
      </details>
      <railgrid-provider-agents><TechnicalDetails diagnostic="The runner reached the request timeout before the tool replied." compact /></railgrid-provider-agents>
      <a class="review-source-link" href="http://127.0.0.1:5200/">Open Agents run fixture</a>
    </section>
    <AIActivityDisclosureStage />
  </div>
</template>
