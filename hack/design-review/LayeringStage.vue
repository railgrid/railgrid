<script setup>
import { onMounted, ref } from 'vue'
import { Info } from 'lucide-vue-next'
import ActionMenu from '../../provider-sdk/portalkit-vue/ActionMenu.vue'
import FormSelect from '../../provider-sdk/portalkit-vue/FormSelect.vue'
import ModelIDSelector from '../../provider-sdk/agentkit-vue/ModelIDSelector.vue'
import ResourceTableFilter from '../../provider-sdk/portalkit-vue/ResourceTableFilter.vue'
import '../../provider-sdk/portalkit/resource-table-filter'
import ConfirmDialog from '../../provider-sdk/portalkit-vue/ConfirmDialog.vue'
import ToastHost from '../../provider-sdk/portalkit-vue/ToastHost.vue'
import ResourceTimestamp from '../../provider-sdk/portalkit-vue/ResourceTimestamp.vue'
import { confirmDialog } from '../../provider-sdk/portalkit-vue/confirm'
import { toast } from '../../provider-sdk/portalkit-vue/toast'

const region = ref('west')
const result = ref('')
const nativeDialog = ref(null)
const vanillaFilter = ref(null)
const edge = ref('')
const model = ref('')
const choices = [{ value: 'west', label: 'US West' }, { value: 'east', label: 'US East' }]
onMounted(() => { if (vanillaFilter.value) Object.assign(vanillaFilter.value, { label: 'Status', options: [{ value: 'ready', label: 'Ready' }, { value: 'offline', label: 'Offline' }] }) })
async function confirm() {
  const accepted = await confirmDialog({ title: 'Remove connection?', message: 'This sample connection will no longer be available.', danger: true, confirmLabel: 'Remove connection' })
  result.value = accepted ? 'Removal confirmed in the fixture.' : 'Removal canceled.'
}
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">These are real shared components with simulated actions. Open each surface and use the keyboard to inspect focus and dismissal.</p>
    <section class="spec-section spec-stack" aria-labelledby="anchored-surfaces">
      <h3 id="anchored-surfaces">Anchored surfaces</h3>
      <div class="spec-row"><ActionMenu label="More actions for staging-west" :items="[{ id: 'edit', label: 'Edit connection' }, { id: 'remove', label: 'Remove connection', tone: 'danger' }]" @select="result = $event + ' selected in the fixture.'" /><button class="k-btn k-btn--ghost" type="button" data-k-tip="Check connection status" aria-label="Check connection status">Check status</button></div>
      <div class="spec-field spec-fit"><span id="layer-region">Region</span><FormSelect v-model="region" :options="choices" labelledby="layer-region" /></div>
    </section>
    <section class="spec-section spec-stack" aria-labelledby="interrupting-surfaces">
      <h3 id="interrupting-surfaces">Dialog and toast</h3>
      <div class="spec-row"><button class="k-btn k-btn--ghost" type="button" @click="confirm">Open confirmation</button><button class="k-btn k-btn--ghost" type="button" @click="toast('info', 'Connection checked in the fixture.')">Show toast</button></div>
      <p class="spec-note" role="status">{{ result }}</p>
      <ConfirmDialog /><ToastHost />
    </section>
    <section class="spec-section spec-stack" aria-labelledby="native-dialog-title">
      <h3 id="native-dialog-title">Controls inside a native dialog</h3>
      <p class="spec-note">Open the dialog, then try the menu, select, and filters with the keyboard. Escape closes an open menu before the dialog.</p>
      <div><button type="button" class="k-btn k-btn--ghost" @click="nativeDialog?.showModal()">Open native dialog</button></div>
      <dialog ref="nativeDialog" class="k-modal" @keydown.esc="nativeDialog?.close()">
        <div class="spec-stack">
          <h3>Connection options</h3>
          <ActionMenu label="Actions inside dialog" :items="[{ id: 'edit', label: 'Edit connection' }, { id: 'remove', label: 'Remove connection' }]" @select="result = $event + ' selected in the fixture.'" />
          <button type="button" class="k-icon-action" aria-label="Inspect connection" data-k-tip="Inspect connection"><Info :size="16" aria-hidden="true" /></button>
          <span>Last checked <ResourceTimestamp value="2026-11-12T14:30:00Z" label="Last checked" /></span>
          <div class="spec-field"><span id="dialog-region">Region</span><FormSelect v-model="region" :options="choices" labelledby="dialog-region" /></div>
          <div class="spec-field"><label for="model-id">Model ID</label><ModelIDSelector v-model="model" :models="[{ id: 'sample-model', name: 'Sample model', compatibility: 'available' }]" /></div>
          <ResourceTableFilter v-model="edge" :definition="{ key: 'edge', label: 'Edge', control: 'combobox' }" :options="[{ value: 'staging-west', label: 'staging-west' }, { value: 'production-east', label: 'production-east' }]" />
          <railgrid-resource-table-filter ref="vanillaFilter"></railgrid-resource-table-filter>
          <div><button type="button" class="k-btn k-btn--ghost" @click="nativeDialog?.close()">Close dialog</button></div>
        </div>
      </dialog>
    </section>
    <div class="spec-row"><a href="http://127.0.0.1:5199/" target="_blank" rel="noopener">Edge Services fixture</a><a href="http://127.0.0.1:5200/" target="_blank" rel="noopener">Agent run fixture</a></div>
  </div>
</template>
