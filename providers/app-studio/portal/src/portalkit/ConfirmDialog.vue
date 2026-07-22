<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Mount ONE instance at the app root; it renders whenever confirmDialog()
     sets confirmState.open. Enter confirms, Escape/backdrop cancels. Styles are
     scoped + token-based, so the component is self-contained and drops into any
     Vue provider portal (Tailwind or plain-CSS) without extra global rules. -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { confirmState, resolveConfirm } from './confirm'

const confirmBtn = ref<HTMLButtonElement | null>(null)

// Render the message as discrete paragraphs so a multi-line message reads
// cleanly instead of as one run-on line.
const paragraphs = computed(() =>
  confirmState.message.split('\n').map((s) => s.trim()).filter(Boolean),
)

function onConfirm() {
  resolveConfirm(true)
}
function onCancel() {
  resolveConfirm(false)
}
function onKeydown(e: KeyboardEvent) {
  if (!confirmState.open) return
  if (e.key === 'Escape') {
    e.preventDefault()
    onCancel()
  } else if (e.key === 'Enter') {
    e.preventDefault()
    onConfirm()
  }
}

watch(
  () => confirmState.open,
  (open) => {
    if (open) {
      window.addEventListener('keydown', onKeydown)
      nextTick(() => confirmBtn.value?.focus())
    } else {
      window.removeEventListener('keydown', onKeydown)
    }
  },
)
</script>

<template>
  <div v-if="confirmState.open" class="pk-overlay" @click.self="onCancel">
    <div class="pk-modal" :class="{ danger: confirmState.danger }" role="alertdialog" aria-modal="true" aria-labelledby="pk-modal-title">
      <h3 id="pk-modal-title" class="pk-title">{{ confirmState.title }}</h3>
      <p v-for="(line, i) in paragraphs" :key="i" class="pk-message">{{ line }}</p>
      <div class="pk-actions">
        <button type="button" class="pk-btn cancel" @click="onCancel">{{ confirmState.cancelLabel }}</button>
        <button
          ref="confirmBtn"
          type="button"
          class="pk-btn confirm"
          :class="{ danger: confirmState.danger }"
          @click="onConfirm"
        >{{ confirmState.confirmLabel }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pk-overlay {
  position: fixed;
  inset: 0;
  z-index: 2147483000;
  display: grid;
  place-items: center;
  padding: 24px;
  background: color-mix(in srgb, var(--color-text-primary, #0b1220) 40%, transparent);
  backdrop-filter: blur(2px);
  animation: pk-fade 0.14s ease;
}
.pk-modal {
  width: min(440px, 100%);
  padding: 22px 22px 18px;
  border-radius: 16px;
  background: var(--color-surface-raised, #fff);
  color: var(--color-text-primary, #10151f);
  border: 1px solid var(--color-border-default, rgba(0, 0, 0, 0.12));
  box-shadow: 0 24px 70px rgba(10, 20, 40, 0.42);
  font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, sans-serif;
  animation: pk-rise 0.18s cubic-bezier(0.2, 0.8, 0.3, 1);
}
.pk-title {
  margin: 0 0 6px;
  font-size: 16px;
  font-weight: 650;
}
.pk-message {
  margin: 0 0 6px;
  color: var(--color-text-secondary, #4a5568);
  overflow-wrap: anywhere;
}
.pk-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 16px;
}
.pk-btn {
  padding: 8px 16px;
  border-radius: 9px;
  border: 0;
  cursor: pointer;
  font: inherit;
  font-weight: 600;
  font-size: 13.5px;
}
.pk-btn.cancel {
  background: var(--color-surface-overlay, rgba(0, 0, 0, 0.05));
  color: var(--color-text-secondary, #4a5568);
  border: 1px solid var(--color-border-default, transparent);
}
.pk-btn.cancel:hover {
  background: var(--color-surface-hover, rgba(0, 0, 0, 0.08));
  color: var(--color-text-primary, #10151f);
}
.pk-btn.confirm {
  background: var(--color-accent, #6d4fe0);
  color: #fff;
}
.pk-btn.confirm:hover {
  background: var(--color-accent-hover, #5b3fd0);
}
.pk-btn.confirm.danger {
  background: var(--color-danger, #ef4444);
}
.pk-btn.confirm.danger:hover {
  background: var(--color-danger-hover, #dc2626);
}
@keyframes pk-fade {
  from {
    opacity: 0;
  }
}
@keyframes pk-rise {
  from {
    opacity: 0;
    transform: translateY(10px) scale(0.98);
  }
}
@media (prefers-reduced-motion: reduce) {
  .pk-overlay,
  .pk-modal {
    animation: none;
  }
}
</style>
