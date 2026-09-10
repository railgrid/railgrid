<script setup lang="ts">
import type { AgentWorkbenchTabKind } from '../agent-workbench'

const props = defineProps<{
  active: AgentWorkbenchTabKind
}>()

function isActive(tab: AgentWorkbenchTabKind): boolean {
  return props.active === tab
}
</script>

<template>
  <div class="agents-workbench" data-agent-workbench>
    <section
      id="agents-workbench-panel-config"
      class="agents-workbench-panel"
      role="tabpanel"
      aria-labelledby="agents-workbench-tab-config"
      :class="{ 'is-active': isActive('config') }"
      :aria-hidden="!isActive('config') ? 'true' : undefined"
      v-show="isActive('config')"
    >
      <slot name="config" />
    </section>
    <!-- AgentConfig owns the state for all three configuration areas. In the
         detail workbench its tools and automation cards teleport into these
         retained panel targets, so tab changes never remount a form. -->
    <section
      id="agents-workbench-panel-tools"
      class="agents-workbench-panel"
      role="tabpanel"
      aria-labelledby="agents-workbench-tab-tools"
      :class="{ 'is-active': isActive('tools') }"
      :aria-hidden="!isActive('tools') ? 'true' : undefined"
      v-show="isActive('tools')"
    >
      <slot name="tools" />
    </section>
    <section
      id="agents-workbench-panel-automation"
      class="agents-workbench-panel"
      role="tabpanel"
      aria-labelledby="agents-workbench-tab-automation"
      :class="{ 'is-active': isActive('automation') }"
      :aria-hidden="!isActive('automation') ? 'true' : undefined"
      v-show="isActive('automation')"
    >
      <slot name="automation" />
    </section>
    <section
      id="agents-workbench-panel-runs"
      class="agents-workbench-panel"
      role="tabpanel"
      aria-labelledby="agents-workbench-tab-runs"
      :class="{ 'is-active': isActive('runs') }"
      :aria-hidden="!isActive('runs') ? 'true' : undefined"
      v-show="isActive('runs')"
    >
      <slot name="runs" />
    </section>
    <section
      id="agents-workbench-panel-launcher"
      class="agents-workbench-panel"
      role="tabpanel"
      aria-labelledby="agents-workbench-tab-launcher"
      :class="{ 'is-active': isActive('launcher') }"
      :aria-hidden="!isActive('launcher') ? 'true' : undefined"
      v-show="isActive('launcher')"
    >
      <slot name="launcher" />
    </section>
  </div>
</template>
