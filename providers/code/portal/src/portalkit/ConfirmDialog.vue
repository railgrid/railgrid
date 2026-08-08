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
  /* Scrim derives from the SURFACE, not text — a text-derived scrim inverts
     to near-white under the dark-default theme. */
  background: color-mix(in srgb, var(--color-surface, #0a0b12) 60%, transparent);
  backdrop-filter: blur(2px);
  animation: pk-fade 0.14s ease;
}
.pk-modal {
  width: min(440px, 100%);
  padding: 22px 22px 18px;
  border-radius: 6px;
  /* Fallbacks are the DARK-BASE token values — dark is the system default. */
  background: var(--color-surface-raised, #111320);
  color: var(--color-text-primary, #e9e9f2);
  border: 1px solid var(--color-border-default, rgba(255, 255, 255, 0.11));
  box-shadow:
    0 1px 2px rgba(0, 0, 0, 0.4),
    0 24px 70px rgba(0, 0, 0, 0.55);
  font-family: var(--font-sans, ui-sans-serif, system-ui, sans-serif);
  font-size: 14px;
  line-height: 1.5;
  animation: pk-rise 0.18s cubic-bezier(0.2, 0.8, 0.3, 1);
}
.pk-title {
  margin: 0 0 6px;
  font-size: 16px;
  font-weight: 650;
}
.pk-message {
  margin: 0 0 6px;
  color: var(--color-text-secondary, #8a8ca6);
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
  border-radius: 4px;
  border: 0;
  cursor: pointer;
  font: inherit;
  font-weight: 600;
  font-size: 13.5px;
}
.pk-btn.cancel {
  background: var(--color-surface-overlay, #171927);
  color: var(--color-text-secondary, #8a8ca6);
  border: 1px solid var(--color-border-default, rgba(255, 255, 255, 0.11));
}
.pk-btn.cancel:hover {
  background: var(--color-surface-hover, #1e2033);
  color: var(--color-text-primary, #e9e9f2);
}
.pk-btn.confirm {
  background: var(--color-accent, #8b6bff);
  color: #fff;
  box-shadow: 0 0 16px var(--color-accent-glow, rgba(139, 107, 255, 0.3));
}
.pk-btn.confirm:hover {
  background: var(--color-accent-hover, #a18aff);
}
.pk-btn.confirm.danger {
  background: var(--color-danger, #ff5d5d);
  box-shadow: none;
}
.pk-btn.confirm.danger:hover {
  background: var(--color-danger-hover, #ff7676);
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
