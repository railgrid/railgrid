import '@fontsource-variable/instrument-sans'
import '@fontsource-variable/archivo/wdth.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import '../../portal/src/assets/main.css'
import './material-sample.css'

const params = new URLSearchParams(location.search)
document.documentElement.classList.toggle('light', params.get('theme') !== 'dark')
const root = document.querySelector('#sample')
if (params.get('kind') === 'type') {
  root.innerHTML = `
    <div class="sample-types">
      <div class="sample-type"><span class="sample-label">Resource detail title</span><h1 class="k-resource-page__title">Production deployment</h1><small>Instrument Sans · 18px / 600</small></div>
      <div class="sample-type"><span class="sample-label">Collection title</span><div class="k-resource-page--collection"><header class="k-resource-page__header"><h2 class="k-resource-page__title">Connections</h2></header></div><small>Instrument Sans · 15px / 600</small></div>
      <div class="sample-type"><span class="sample-label">Section title</span><h2 class="k-resource-section-card__title">Rollout status</h2><small>Instrument Sans · 14px / 600</small></div>
      <div class="sample-type"><span class="sample-label">Body and supporting copy</span><p class="sample-copy">Your deployment is ready. Review the service before promoting it.</p><small>Instrument Sans · 13px</small></div>
      <div class="sample-type"><span class="sample-label">Action</span><span class="k-btn k-btn--ghost">View deployment</span><small>Shared button · 13px / 600</small></div>
      <div class="sample-type"><span class="sample-label">Metadata and helper</span><p class="sample-helper">Last checked 2 minutes ago · Production</p><small>Secondary text · 12px</small></div>
      <div class="sample-type"><span class="sample-label">Technical value</span><code>edge/production-east · 12:42:08 UTC</code><small>IBM Plex Mono · 12px</small></div>
      <div class="sample-type"><span class="sample-label">Metric</span><span class="k-kpi">1,028</span><small>Instrument Sans · 26px / tabular</small></div>
      <div class="sample-type"><span class="sample-label">Brand display</span><span class="sample-brand">RAILGRID</span><small>Archivo · brand use</small></div>
      <section class="sample-ink-roles" aria-labelledby="text-color-roles">
        <h2 id="text-color-roles">Text color roles</h2>
        <p class="sample-ink-primary">Primary · Resource names and important values <code>--color-text-primary</code></p>
        <p class="sample-ink-secondary">Secondary · Supporting explanations <code>--color-text-secondary</code></p>
        <p class="sample-ink-muted">Muted · Labels and metadata <code>--color-text-muted</code></p>
        <p class="sample-ink-accent">Accent · Links and active choices <code>--color-accent</code></p>
        <p class="sample-ink-success">Success · Ready <code>--color-success</code></p>
        <p class="sample-ink-warning">Warning · Needs attention <code>--color-warning</code></p>
        <p class="sample-ink-danger">Danger · Failed <code>--color-danger</code></p>
        <p class="sample-ink-hover">Accent on hover surface · View details <code>--color-accent-hover on --color-surface-hover</code></p>
      </section>
    </div>`
} else {
  root.innerHTML = `
    <div class="sample-surface-page">
      <div class="sample-surface-heading"><span>Page</span><code>surface</code></div>
      <div class="k-card sample-card"><div class="sample-surface-heading"><span>Raised section</span><code>k-card</code></div><p>Service health and configuration.</p><div class="sample-inset"><strong>Inset content</strong><span>Read-only technical details</span></div><div class="sample-hover">Hover surface · interaction state</div></div>
      <div class="k-card k-card--flat sample-card"><div class="sample-surface-heading"><span>Flat section</span><code>k-card--flat</code></div><p>Same surface and border, without the shadow.</p></div>
      <div class="sample-floating-grid">
        <div class="k-menu sample-menu"><div class="sample-surface-heading"><span>Menu</span><code>k-menu</code></div><div class="k-menu-item">View details</div><div class="k-menu-item">Edit service</div></div>
        <div class="sample-tooltip-wrap"><span class="sample-label">Tooltip</span><div class="k-tooltip-layer sample-tooltip">Copy run ID</div></div>
      </div>
      <div class="sample-dialog-scene">
        <div class="sample-dialog-background" aria-hidden="true"><strong>Services</strong><p>home-assistant · Ready</p><p>grafana · Ready</p></div>
        <div class="k-modal-overlay sample-dialog-scrim"><div class="k-modal sample-modal"><div class="sample-surface-heading"><span>Dialog and scrim</span><code>k-modal + k-modal-overlay</code></div><h2>Delete service</h2><p>Review the consequences before continuing.</p><div class="sample-modal-actions"><span class="k-btn k-btn--ghost">Cancel</span><span class="k-btn k-btn--danger">Delete</span></div></div></div>
      </div>
    </div>`
}
