import { createApp, h, ref } from 'vue'
import ProjectShareDialog from '../../providers/app-studio/portal/src/ProjectShareDialog.vue'
import ConfirmDialog from '../../providers/app-studio/portal/src/portalkit/ConfirmDialog.vue'
import { confirmDialog } from '../../providers/app-studio/portal/src/portalkit/confirm'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '@/assets/main.css'
import '../../providers/app-studio/portal/src/style.css'
import './kuery-impact.css'

export const app = createApp({
  setup() {
    const open = ref(false), theme = ref('light'), scenario = ref('ready')
    const published = ref(true), deployed = ref(true)
    const mode = ref('restricted'), savedMode = ref('restricted')
    const previewMode = ref('restricted'), previewSavedMode = ref('restricted')
    const grants = ref([{ name: 'reviewer', user: 'reviewer@example.com', publication: 'production', publicationUID: 'fixture', revoked: false }])
    const previewGrants = ref([{ name: 'preview-reviewer', user: 'designer@example.com', publication: 'preview', publicationUID: 'fixture', revoked: false }])
    const add = (list, user, publication) => list.value.push({ name: user, user, publication, publicationUID: 'fixture', revoked: false })
    const disable = async () => {
      if (!await confirmDialog({ title: 'Disable external access?', message: 'Nobody will be able to access the production URL. The production deployment will keep running.', confirmLabel: 'Disable access', danger: true })) return
      published.value = false
      mode.value = 'restricted'
      open.value = false
    }
    return () => h('main', { class: 'impact-fixture' }, [
      h('div', { class: 'impact-fixture__controls' }, [
        h('a', { class: 'k-btn k-btn--ghost', href: '/?entry=design.journeys.app-studio-sharing' }, 'Back to sharing guidance'),
        h('button', { class: 'k-btn k-btn--ghost', onClick: () => { theme.value = theme.value === 'light' ? 'dark' : 'light'; document.documentElement.classList.toggle('light', theme.value === 'light') } }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
        h('label', ['Scenario ', h('select', { class: 'k-input', value: scenario.value, onChange: e => { scenario.value = e.target.value; deployed.value = scenario.value !== 'undeployed' } }, [h('option', { value: 'ready' }, 'Ready'), h('option', { value: 'partial' }, 'Viewer refresh failed'), h('option', { value: 'pending' }, 'Preview applying'), h('option', { value: 'undeployed' }, 'Production not deployed')])]),
      ]),
      h('p', { class: 'impact-fixture__note' }, 'Simulated project and access settings. Actions update this fixture only; no links or access grants are real.'),
      h('button', { class: 'k-btn k-btn--primary', onClick: () => { open.value = true } }, 'Share project'),
      h(ProjectShareDialog, {
        open: open.value, projectName: 'Field notes', mode: mode.value, published: deployed.value && published.value, publicationStateAvailable: true,
        publication: deployed.value && published.value ? { mode: savedMode.value, ready: true } : null, productionURL: 'https://field-notes.example.com', productionReady: deployed.value,
        members: [{ user: 'alex@example.com' }, { user: 'sam@example.com' }], grants: grants.value,
        previewSupported: true, previewURL: 'https://preview.example.com/field-notes', previewMode: previewMode.value, previewSavedMode: previewSavedMode.value,
        previewConverged: scenario.value !== 'pending', previewGrants: previewGrants.value,
        loadState: scenario.value === 'partial' ? 'partial' : 'ready', membersError: scenario.value === 'partial' ? 'Could not refresh workspace members.' : null,
        'onUpdate:mode': value => { mode.value = value }, 'onUpdate:previewMode': value => { previewMode.value = value },
        onSave: () => { savedMode.value = mode.value; published.value = true }, onSavePreview: () => { previewSavedMode.value = previewMode.value },
        onGrant: user => add(grants, user, 'production'), onInvite: user => add(grants, user, 'production'), onRevoke: name => { grants.value = grants.value.filter(g => g.name !== name) },
        onPreviewGrant: user => add(previewGrants, user, 'preview'), onPreviewInvite: user => add(previewGrants, user, 'preview'), onPreviewRevoke: name => { previewGrants.value = previewGrants.value.filter(g => g.name !== name) },
        onRetry: () => { scenario.value = 'ready' }, onClose: () => { open.value = false }, onOpenPublishing: () => { open.value = false }, onDisable: disable,
      }),
      h(ConfirmDialog),
    ])
  },
})
app.mount('#app')
