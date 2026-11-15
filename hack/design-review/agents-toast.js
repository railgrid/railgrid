import { createApp, h, ref } from 'vue'
import { clearToasts, toast } from '../../provider-sdk/portalkit/toast'
import '@fontsource-variable/instrument-sans'
import '@/assets/main.css'
import './kuery-impact.css'

createApp({
  setup() {
    const theme = ref('light')
    const outcome = ref('')
    function toggleTheme() {
      theme.value = theme.value === 'light' ? 'dark' : 'light'
      document.documentElement.classList.toggle('light', theme.value === 'light')
    }
    function showRetry() {
      let attempts = 0
      toast('error', 'Could not refresh the run.', { label: 'Retry', run: async () => {
        await new Promise(resolve => setTimeout(resolve, 1500))
        if (++attempts === 1) throw new Error('Simulated failure')
        outcome.value = 'Run refreshed in the fixture.'
      } })
    }
    return () => h('main', { class: 'impact-fixture' }, [
      h('div', { class: 'impact-fixture__controls' }, [
        h('a', { href: '/?entry=design.components.toast', class: 'k-btn k-btn--ghost' }, 'Back to toast review'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: toggleTheme }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
      ]),
      h('h1', 'Agents notification examples'),
      h('p', { class: 'impact-fixture__note' }, 'Uses the same framework-neutral renderer as Agents. All messages and actions are simulated.'),
      h('div', { class: 'impact-fixture__controls' }, [
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => toast('ok', 'Settings saved.') }, 'Show success'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => toast('error', 'Could not save. Check the settings and try again.') }, 'Show error'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => toast('info', 'Run queued.', { label: 'View run', run: () => { outcome.value = 'Run selected in the fixture.' } }) }, 'Show action'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: showRetry }, 'Show retry failure'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { clearToasts(); for (let i = 1; i <= 10; i++) toast('error', `Sample issue ${i} needs review.`) } }, 'Show notification backlog'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { toast('error', 'Sample issue awaiting review.'); outcome.value = 'Focus the notification before it clears in three seconds.'; setTimeout(() => { clearToasts(); outcome.value = 'Notifications cleared.' }, 3000) } }, 'Review timed clear'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: clearToasts }, 'Clear notifications'),
      ]),
      h('p', { role: 'status', class: 'impact-fixture__note' }, outcome.value),
    ])
  },
}).mount('#app')
