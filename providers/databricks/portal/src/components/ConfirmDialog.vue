<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { AlertTriangle, X } from 'lucide-vue-next'

defineProps<{
  title: string
  message: string
  confirmLabel?: string
  busy?: boolean
}>()

const emit = defineEmits<{
  confirm: []
  cancel: []
}>()

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    emit('cancel')
  }
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <Teleport to="body">
    <div
      class="databricks-modal-overlay"
      @click.self="emit('cancel')"
    >
      <div class="databricks-modal">
        <div class="databricks-modal-head">
          <div class="databricks-modal-title-row">
            <div class="databricks-modal-icon-tile">
              <AlertTriangle class="databricks-modal-icon" :stroke-width="1.75" />
            </div>
            <div>
              <h2 class="databricks-modal-title">{{ title }}</h2>
              <p class="databricks-modal-message">{{ message }}</p>
            </div>
          </div>
          <button
            class="databricks-modal-close"
            :disabled="busy"
            @click="emit('cancel')"
          >
            <X class="databricks-modal-close-icon" :stroke-width="2" />
          </button>
        </div>

        <div class="databricks-modal-actions">
          <button
            class="databricks-modal-cancel"
            :disabled="busy"
            @click="emit('cancel')"
          >
            Cancel
          </button>
          <button
            class="databricks-modal-confirm"
            :disabled="busy"
            @click="emit('confirm')"
          >
            {{ busy ? 'Working...' : confirmLabel ?? 'Confirm' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
