<script setup lang="ts">
import { ref } from 'vue'
import ConfirmDialog from '../../provider-sdk/portalkit-vue/ConfirmDialog.vue'
import { confirmDialog } from '../../provider-sdk/portalkit-vue/confirm'
import { confirmModal } from '../../provider-sdk/portalkit/modal'

const outcome = ref('')

async function deleteInstance() {
  const confirmed = await confirmDialog({
    title: 'Delete instance',
    message: 'api-production and the resources it provisioned will be permanently deleted.',
    irreversibleMessage: 'Deleting api-production cannot be undone.',
    confirmLabel: 'Delete instance',
    danger: true,
    verificationText: 'api-production',
    verificationLabel: 'the instance name',
  })
  outcome.value = confirmed ? 'Sample instance deleted.' : 'Instance deletion cancelled.'
}

async function removeService() {
  const confirmed = await confirmDialog({
    title: 'Remove service',
    message: 'Remove the sample service from this Edge?',
    confirmLabel: 'Remove service',
    danger: true,
  })
  outcome.value = confirmed ? 'Sample service removed.' : 'Service removal cancelled.'
}

async function disconnect() {
  const confirmed = await confirmModal({
    title: 'Disconnect sample integration',
    message: 'Disconnect this integration from the workspace?',
    confirmLabel: 'Disconnect',
    danger: true,
  })
  outcome.value = confirmed ? 'Sample integration disconnected.' : 'Disconnect cancelled.'
}
</script>

<template>
  <div class="spec-stack">
    <div class="spec-row">
      <button class="k-btn k-btn--ghost" type="button" @click="removeService">Remove service</button>
      <button class="k-btn k-btn--ghost" type="button" @click="deleteInstance">Delete instance</button>
      <button class="k-btn k-btn--ghost" type="button" @click="disconnect">Disconnect sample integration</button>
    </div>
    <p class="spec-note" role="status">{{ outcome }}</p>
    <ConfirmDialog />
  </div>
</template>
