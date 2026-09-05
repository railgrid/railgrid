<script setup lang="ts">
import { computed } from 'vue'
import { KeyRound } from 'lucide-vue-next'
import { APPROVAL_DISCLOSURE_ERROR, approvalDisclosure } from '../approval-disclosure'

const props = withDefaults(defineProps<{
  tool?: unknown
  args?: unknown
  paused?: boolean
}>(), { tool: undefined, args: undefined, paused: false })

const disclosure = computed(() => approvalDisclosure(props.tool, props.args))
</script>

<template>
  <div class="agents-approval-disclosure">
    <div class="agents-approval-head">
      <KeyRound :stroke-width="1.75" aria-hidden="true" />
      {{ paused ? 'Paused — approval required for' : 'Approval required —' }}
      <span v-if="disclosure.tool" class="mono">{{ disclosure.tool }}</span>
      <span v-else>tool unavailable</span>
    </div>
    <pre v-if="disclosure.argsAvailable" class="agents-approval-args">{{ disclosure.formattedArgs }}</pre>
    <div v-if="!disclosure.valid" class="agents-err agents-approval-disclosure-error" role="alert">
      {{ APPROVAL_DISCLOSURE_ERROR }}
    </div>
  </div>
</template>
