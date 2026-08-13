<template>
  <div
    class="grid min-h-[410px] content-start gap-4 rounded-md border border-border-subtle bg-surface-overlay/50 p-3"
    role="status"
    aria-live="polite"
    aria-busy="true"
  >
    <span class="sr-only">Loading production settings…</span>
    <div class="grid gap-3" aria-hidden="true">
      <div class="grid grid-cols-4 gap-2">
        <div v-for="step in 4" :key="step" class="shimmer h-10 rounded bg-surface-overlay" />
      </div>
      <div class="shimmer h-4 w-2/3 rounded bg-surface-overlay" />
      <div class="shimmer h-3 w-full rounded bg-surface-overlay" />
      <div class="shimmer h-3 w-4/5 rounded bg-surface-overlay" />
      <div class="mt-2 grid gap-3 border-t border-border-subtle pt-4">
        <div class="shimmer h-4 w-32 rounded bg-surface-overlay" />
        <div class="shimmer h-9 w-full rounded bg-surface-overlay" />
        <div class="shimmer h-9 w-3/4 rounded bg-surface-overlay" />
      </div>
    </div>
  </div>
</template>
