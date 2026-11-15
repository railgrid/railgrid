<script setup>
import { computed, onUnmounted, ref } from 'vue'
import AISkillCatalogList from '../../provider-sdk/agentkit-vue/AISkillCatalogList.vue'
import SkillsWorkbench from '../../providers/app-studio/portal/src/SkillsWorkbench.vue'
import studioStyles from '../../providers/app-studio/portal/src/style.css?inline'

// The real provider installs these utilities within its own root. The review
// mounts the component directly, including its teleported dialog, so give both
// fixture surfaces the same scoped stylesheet without changing the book shell.
const style = document.createElement('style')
style.textContent = `@scope (.studio-skill-fixture, #app-studio-overlay-root) { ${studioStyles.replace(/--color-[\w-]+:var\(--color[^;}]*;?/g, '')} }`
document.head.appendChild(style)
onUnmounted(() => style.remove())

const query = ref('')
const selected = ref('')
const state = ref('ready')
const variant = ref('catalog')
const skills = [
  { id: 'rollout-review', name: 'Rollout review', description: 'Check rollout health and recovery options.', scope: 'workspace', enabled: true },
  { id: 'incident-summary', name: 'Incident summary', description: 'Summarize recent investigation evidence.', scope: 'workspace', enabled: false },
  { id: 'edge-health', name: 'Edge health', description: 'Inspect connectivity and service status.', scope: 'provider', enabled: true },
]
const activationOutcome = ref('failure')
let studioSkills = skills.map(skill => ({ ...skill }))
let refreshCalls = 0
const visibleSkills = computed(() => state.value === 'empty' || state.value === 'loading' ? [] : skills)
const studioContext = {
  tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace',
  fetch: async (input, init = {}) => {
    const url = new URL(String(input), window.location.origin)
    if ((!init.method || init.method === 'GET') && url.pathname.endsWith('/skill-detail')) {
      const skill = skills.find(item => item.id === url.searchParams.get('id'))
      if (skill) return new Response(JSON.stringify({ ...skill, instructions: 'Review health signals, identify failures, and report a safe recovery path.', resources: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (init.method === 'POST' && url.pathname.endsWith('/skills-activation')) {
      await new Promise(resolve => setTimeout(resolve, 800))
      if (activationOutcome.value === 'failure') return new Response(JSON.stringify({ message: 'Could not update skill activation. Try again.' }), { status: 403, headers: { 'Content-Type': 'application/json' } })
      const { id, enabled } = JSON.parse(init.body)
      studioSkills = studioSkills.map(skill => skill.id === id ? { ...skill, enabled } : skill)
      return new Response(JSON.stringify(studioSkills.find(skill => skill.id === id)), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if ((!init.method || init.method === 'GET') && url.pathname.endsWith('/skills') && activationOutcome.value !== 'failure') {
      const fail = activationOutcome.value === 'refresh-failure' && refreshCalls++ === 0
      return new Response(JSON.stringify(fail ? { message: 'Could not refresh skills. Try again.' } : { skills: studioSkills }), { status: fail ? 503 : 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ message: 'This review fixture does not change project skills.' }), { status: 403, headers: { 'Content-Type': 'application/json' } })
  },
}
</script>

<template>
  <div class="studio-skill-fixture spec-stack">
    <p class="spec-note">Sample data only. Selecting a skill displays its name; it does not enable or execute it.</p>
    <div class="spec-row">
      <label class="spec-field spec-fit">Layout<select v-model="variant" class="k-input"><option value="catalog">Catalog</option><option value="workbench">Workbench</option></select></label>
      <label class="spec-field spec-fit">Data state<select v-model="state" class="k-input"><option value="ready">Loaded</option><option value="refreshing">Refreshing</option><option value="loading">Loading</option><option value="empty">Empty</option></select></label>
    </div>
    <section class="spec-section" aria-label="Skill catalog example">
      <AISkillCatalogList v-model:query="query" :skills="visibleSkills" :selected-i-d="selected" :loading="state === 'loading' || state === 'refreshing'" :variant="variant" @select="selected = $event.id" />
    </section>
    <p class="spec-note" role="status">{{ selected ? `Selected: ${selected}` : 'Choose a skill to inspect its details.' }}</p>
    <section class="studio-skill-fixture spec-section spec-stack" aria-labelledby="studio-skills-heading">
      <div><h3 id="studio-skills-heading">App Studio workbench</h3><p class="spec-note">The product component opens a detail dialog with sample data. Changes stay in this fixture.</p></div>
      <label class="spec-field spec-fit">Activation result<select v-model="activationOutcome" class="k-input"><option value="failure">Save fails</option><option value="success">Save and refresh succeed</option><option value="refresh-failure">Save succeeds; first refresh fails</option></select></label>
      <SkillsWorkbench :ctx="studioContext" project-name="sample-project" :skills="skills" />
    </section>
  </div>
</template>
