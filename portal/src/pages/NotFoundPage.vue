<script setup lang="ts">
import { Hexagon, ArrowLeft } from 'lucide-vue-next'
</script>

<template>
  <div class="cross-grid relative flex min-h-screen items-center justify-center bg-surface">
    <div class="pointer-events-none fixed inset-0 overflow-hidden">
      <div class="absolute top-1/3 left-1/2 h-72 w-72 -translate-x-1/2 rounded-full bg-accent/4 blur-[140px]" />
    </div>

    <div class="relative text-center">
      <div class="relative mx-auto flex h-20 w-20 items-center justify-center">
        <div class="absolute inset-0 rounded-2xl bg-accent/10 blur-lg" />
        <div class="relative flex h-20 w-20 items-center justify-center rounded-2xl border border-border-subtle bg-surface-raised/80 backdrop-blur">
          <Hexagon class="h-10 w-10 text-text-muted/15" :stroke-width="1" />
        </div>
      </div>
      <h1 class="text-gradient mt-6 text-7xl font-bold tracking-tighter">404</h1>
      <p class="mt-2 text-[13px] text-text-muted">This page doesn't exist</p>
      <router-link
        to="/"
        class="mt-8 inline-flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-5 py-2.5 text-[12px] font-medium text-text-secondary backdrop-blur transition-all duration-200 hover:border-accent/30 hover:text-text-primary"
      >
        <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" />
        Back to dashboard
      </router-link>
    </div>
  </div>
</template>
