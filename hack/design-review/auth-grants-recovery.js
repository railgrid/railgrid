import { createApp, h, ref } from 'vue'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '../../portal/src/assets/main.css'
import '../../providers/app-studio/portal/src/style.css'
import ProjectAuthentication from '../../providers/app-studio/portal/src/ProjectAuthentication.vue'
import { authenticationApi as api } from '../../providers/app-studio/portal/src/authenticationApi'

let reads = 0
api.getProjectAuthentication = async () => ({ enabled: true, supported: true, applicationVerified: true, message: '', environments: [{ environment: 'production', configured: true, runtimeExists: true, runtimeReady: true }] })
api.getProjectAuthenticationGrants = async () => {
  reads += 1
  if (reads === 1) throw new Error('Simulated approval read failure')
  await new Promise(resolve => setTimeout(resolve, 1200))
  return { items: [] }
}
const rejectMutation = async () => { throw new Error('This review fixture cannot change authentication.') }
api.configureProjectAuthentication = rejectMutation
api.addProjectAuthenticationGrant = rejectMutation
api.removeProjectAuthenticationGrant = rejectMutation
createApp({
  setup() {
    const dark = ref(false)
    return () => h('div', { style: 'padding:24px' }, [
      h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { dark.value = !dark.value; document.documentElement.classList.toggle('light', !dark.value) } }, `Switch to ${dark.value ? 'light' : 'dark'} theme`),
      h('p', 'Synthetic authentication state. No access is changed or hub contacted.'),
      h('railgrid-provider-app-studio', [h(ProjectAuthentication, { ctx: null, project: { uid: 'sample', name: 'review-project' } })]),
    ])
  },
}).mount('#app')
