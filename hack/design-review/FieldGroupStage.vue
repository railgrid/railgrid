<script setup>
import { ref } from 'vue'
import CreateGuidance from '../../provider-sdk/portalkit-vue/CreateGuidance.vue'
import '../../providers/agents/portal/src/style.css'
const selected = ref(['visualize'])
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">Anatomy: a group legend, labeled choices with individual descriptions, and one shared explanation. Selecting a choice changes only the fixture.</p>
    <railgrid-provider-agents>
      <fieldset class="agents-cap-fs agents-resource-fieldset" aria-describedby="review-toolset-group-help">
        <legend>Tools</legend>
        <div class="agents-checkrow">
          <label class="agents-check k-checkbox-hit"><input v-model="selected" class="k-checkbox" type="checkbox" value="visualize"><span class="agents-check-copy"><span>Visualize data</span><span class="agents-hint">Charts in chat from supplied data</span></span></label>
          <label class="agents-check k-checkbox-hit"><input v-model="selected" class="k-checkbox" type="checkbox" value="github"><span class="agents-check-copy"><span>GitHub</span><span class="agents-hint">Issues, pull requests, and code search</span></span></label>
        </div>
        <span id="review-toolset-group-help" class="agents-hint">Choose visualization and any available tool connections.</span>
      </fieldset>
    </railgrid-provider-agents>
    <CreateGuidance title="Before you create" description="Creating a toolset does not change existing agents. Attach it in agent Config to grant access." />
    <p class="spec-note" role="status">{{ selected.length }} selected in this example.</p>
  </div>
</template>
