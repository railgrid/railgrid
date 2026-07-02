<script setup lang="ts">
import PortalConfirmDialog from './ConfirmDialog.vue'
import { confirmState, resolveConfirm } from './confirm'
</script>

<template>
  <PortalConfirmDialog
    v-if="confirmState.open"
    :title="confirmState.title"
    :message="confirmState.message"
    :confirm-label="confirmState.confirmLabel"
    @confirm="resolveConfirm(true)"
    @cancel="resolveConfirm(false)"
  />
</template>
