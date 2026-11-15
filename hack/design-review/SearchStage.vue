<script setup lang="ts">
import { ref, watch } from 'vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'

const state = ref('empty')
const query = ref('')
const rows = [
  { name: 'home-assistant', edge: 'garage-edge' },
  { name: 'grafana', edge: 'ops-edge' },
  { name: 'unifi-controller', edge: 'office-edge' },
]
const columns = [
  { key: 'name', label: 'Service', primary: true },
  { key: 'edge', label: 'Edge' },
]

watch(state, (next) => {
  query.value = next === 'populated' ? 'home' : next === 'no-results' ? 'camera' : ''
})
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="search-state-title">
    <h3 id="search-state-title">Search Edge Services</h3>
    <label class="spec-row">
      Starting example
      <select v-model="state" class="form-select">
        <option value="empty">Empty</option>
        <option value="populated">Populated</option>
        <option value="no-results">No results</option>
        <option value="disabled">Disabled</option>
        <option value="loading">Loading</option>
      </select>
    </label>
    <template v-if="state === 'disabled'">
      <div class="k-table__search">
        <input class="k-table__search-input" type="search" aria-label="Search services" placeholder="Search services…" disabled>
      </div>
      <p class="spec-note">Search is unavailable while service inventory is disconnected.</p>
    </template>
    <ResourceTable
      v-else
      v-model:query="query"
      :rows="rows"
      :columns="columns"
      row-key="name"
      aria-label="Example services"
      :loaded="state !== 'loading'"
      :loading="state === 'loading'"
      :search-keys="['name']"
      search-placeholder="Search services…"
      search-empty-text="No services match your search."
      searchable
    />
  </section>
</template>
