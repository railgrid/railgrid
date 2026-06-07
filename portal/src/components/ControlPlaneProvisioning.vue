<!--
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<!--
First-login provisioning takeover. On a brand-new account the hub's
org-bootstrap controller is still building the personal org, the org
workspace, and the default child workspace (~10-25s cold start; the
workspace's clusterName is omitted from the REST list until it reports
Ready). The tenant store polls until that lands and flips
bootstrapState to 'ready'; until then App.vue mounts this full-screen
overlay so the user sees "creating your control plane" instead of an
empty, cluster-less shell that errors on every query.

Purely presentational — the polling and state live in the tenant store.
The step list is cosmetic, advancing off the poll-attempt count so the
screen feels alive while the controller chain runs.
-->

<script setup lang="ts">
import { computed } from 'vue'
import { Hexagon, Check, Loader2, Building2, Boxes, KeyRound, Sparkles } from 'lucide-vue-next'

// attempts = the tenant store's poll counter (~one every 2s). Used only
// to walk the cosmetic step list and to surface a "taking longer than
// usual" note once we pass the typical cold-start budget.
const props = withDefaults(defineProps<{ attempts?: number }>(), { attempts: 0 })

interface Step {
  label: string
  icon: unknown
}
const steps: Step[] = [
  { label: 'Creating your organization', icon: Building2 },
  { label: 'Provisioning workspace cluster', icon: Boxes },
  { label: 'Binding APIs & permissions', icon: KeyRound },
  { label: 'Finalizing control plane', icon: Sparkles },
]

// Walk one step roughly every ~4s (2 polls). Clamp to the last step so
// the final item keeps spinning until the store flips us to 'ready' and
// App.vue unmounts the overlay.
const activeStep = computed(() => Math.min(Math.floor(props.attempts / 2), steps.length - 1))

// The hub's bootstrap chain is ~10-25s; past ~30s of polling we nudge
// the user that it's taking longer than usual rather than leave them
// staring at an indeterminate spinner.
const overBudget = computed(() => props.attempts >= 15)
</script>

<template>
  <div class="cross-grid fixed inset-0 z-[200] flex items-center justify-center bg-surface">
    <!-- Ambient glow, matching the login / auth-callback takeovers -->
    <div class="pointer-events-none fixed inset-0 overflow-hidden">
      <div class="absolute -top-40 left-1/2 h-96 w-[500px] -translate-x-1/2 rounded-full bg-accent/5 blur-[160px]" />
      <div class="absolute bottom-1/4 right-1/3 h-64 w-64 rounded-full bg-success/4 blur-[140px]" />
    </div>

    <div class="relative flex w-full max-w-md flex-col items-center px-6">
      <!-- Pulsing hex mark -->
      <div class="relative flex h-16 w-16 items-center justify-center">
        <div class="absolute inset-0 animate-pulse rounded-2xl bg-accent/20 blur-lg" />
        <div class="relative flex h-16 w-16 items-center justify-center rounded-2xl border border-accent/25 bg-surface-overlay">
          <Hexagon class="h-8 w-8 animate-spin text-accent" style="animation-duration: 3s" :stroke-width="2" />
        </div>
      </div>

      <h1 class="mt-6 text-center text-[18px] font-bold tracking-tight text-text-primary">
        Creating your control plane
      </h1>
      <p class="mt-1.5 text-center text-[12px] text-text-muted">
        Setting up your organization and first workspace. This is a one-time setup
        and usually takes a few seconds.
      </p>

      <!-- Step list -->
      <div class="border-beam mt-7 w-full rounded-2xl">
        <ul class="space-y-1 rounded-2xl border border-border-subtle bg-surface-raised/80 p-3 backdrop-blur">
          <li
            v-for="(step, i) in steps"
            :key="step.label"
            class="flex items-center gap-3 rounded-xl px-3 py-2 transition-colors"
            :class="i === activeStep ? 'bg-surface-overlay/60' : ''"
          >
            <!-- State glyph: done / active / pending -->
            <span
              class="flex h-6 w-6 shrink-0 items-center justify-center rounded-lg border"
              :class="i < activeStep
                ? 'border-success/30 bg-success-subtle text-success'
                : i === activeStep
                  ? 'border-accent/30 bg-accent/10 text-accent'
                  : 'border-border-default/40 bg-surface-overlay/40 text-text-muted/40'"
            >
              <Check v-if="i < activeStep" class="h-3.5 w-3.5" :stroke-width="2.5" />
              <Loader2 v-else-if="i === activeStep" class="h-3.5 w-3.5 animate-spin" :stroke-width="2.5" />
              <component :is="step.icon" v-else class="h-3.5 w-3.5" :stroke-width="2" />
            </span>
            <span
              class="text-[12px] font-medium"
              :class="i < activeStep
                ? 'text-text-secondary'
                : i === activeStep
                  ? 'text-text-primary'
                  : 'text-text-muted/50'"
            >
              {{ step.label }}
            </span>
          </li>
        </ul>
      </div>

      <Transition name="fade">
        <p v-if="overBudget" class="mt-4 text-center text-[11px] text-text-muted/70">
          Still working — a cold start can take up to a minute. Hang tight.
        </p>
      </Transition>
    </div>
  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
