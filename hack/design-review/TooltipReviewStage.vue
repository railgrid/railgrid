<script setup>
import { ref } from 'vue'
import { Copy } from 'lucide-vue-next'
import ResourceTableEditButton from '../../provider-sdk/portalkit-vue/ResourceTableEditButton.vue'
import { ensureTooltips } from '../../provider-sdk/portalkit/tooltip'

ensureTooltips()
const notice = ref('')
</script>

<template>
  <div class="spec-stack">
    <div class="spec-line">
      <div><strong>Endpoint</strong><p class="spec-note">home-server:8123</p></div>
      <button class="k-icon-action" type="button" aria-label="Copy endpoint address" data-k-tip="Copy endpoint address" @click="notice = 'Copy selected in the fixture.'"><Copy :size="16" :stroke-width="1.75" aria-hidden="true" /></button>
    </div>
    <div class="spec-line">
      <div><strong>home-assistant</strong><p class="spec-note">Service · Ready</p></div>
      <ResourceTableEditButton label="Edit home-assistant service" @click="notice = 'Edit selected in the fixture.'" />
    </div>
    <a class="review-source-link" href="http://127.0.0.1:5199/">Open Edge Services fixture</a>
    <p class="spec-note" role="status">{{ notice }}</p>
  </div>
</template>
