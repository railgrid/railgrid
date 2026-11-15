<script setup>
import { ref } from 'vue'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'
import ResourceSectionCard from '../../provider-sdk/portalkit-vue/ResourceSectionCard.vue'

const saved = ref(false)
</script>

<template>
  <div class="spec-stack spacing-study">
    <p class="spec-note">Compare the shared recipes in context. Spacing values are observed recipe dimensions, not an enforced scale; the radius rules are normative. Sample data does not establish product-wide adoption.</p>

    <section aria-labelledby="spacing-shell-title" class="spacing-study__section">
      <div>
        <h3 id="spacing-shell-title">Page and section rhythm</h3>
        <p class="spec-note">The outline simulates the ordinary shell’s content column. The real AppLayout owns the outer gutter; full-bleed workbenches use a separate contract.</p>
      </div>
      <div class="spacing-study__shell">
        <div class="spacing-study__gutter-label">Shell gutter · 16px narrow / 32px from 640px</div>
        <div class="spacing-study__content">
          <ResourcePage title="staging-west" kind="Edge" subtitle="A sample resource for comparing page and section rhythm.">
            <template #body>
              <ResourceSectionCard id="spacing-connection" title="Connection" description="Details used to reach this edge.">
                <div class="k-create-fields">
                  <label class="spec-field">Display name<input class="k-input" value="Staging west"><small>Shown to workspace members.</small></label>
                  <label class="spec-field">Hostname<input class="k-input" value="staging-west.example.test"></label>
                </div>
              </ResourceSectionCard>
            </template>
          </ResourcePage>
        </div>
      </div>
      <dl class="spacing-study__facts">
        <div><dt>Resource page</dt><dd>18px between page regions; 16px for collections</dd></div>
        <div><dt>Section card</dt><dd>24px padding and 20px internal gap; padding becomes 16px in a narrow resource page</dd></div>
        <div><dt>Fields</dt><dd>14px between fields in the create-fields recipe</dd></div>
      </dl>
    </section>

    <section aria-labelledby="spacing-controls-title" class="spacing-study__section">
      <div><h3 id="spacing-controls-title">Controls and shape</h3><p class="spec-note">Shared controls keep related content close. The examples below use the canonical recipes.</p></div>
      <div class="spacing-study__controls">
        <div><button type="button" class="k-btn k-btn--primary" @click="saved = true">Save changes</button><p>Button · 8px × 14px padding, 8px content gap</p><p v-if="saved" role="status">Saved in this example.</p></div>
        <div><label class="spec-field">Display name<input class="k-input" value="Staging west"></label><p>Input · 8px × 12px padding</p></div>
        <div><span class="k-badge">EDGE</span><p>Tag · 3px radius</p></div>
      </div>
      <div class="spacing-study__shapes" aria-label="Corner radius examples">
        <div><span class="spacing-study__shape spacing-study__shape--tag" aria-hidden="true"></span><strong>Tags</strong><span>3px</span></div>
        <div><span class="spacing-study__shape spacing-study__shape--control" aria-hidden="true"></span><strong>Controls</strong><span>4px</span></div>
        <div><span class="spacing-study__shape spacing-study__shape--panel" aria-hidden="true"></span><strong>Panels</strong><span>6px</span></div>
        <div><span class="spacing-study__shape spacing-study__shape--circle" aria-hidden="true"></span><strong>Circles</strong><span>50%</span></div>
      </div>
      <p class="spec-note">Dots, avatars, spinners and toggle knobs are circles. Conversation bubbles are a documented exception.</p>
    </section>
    <div class="spec-row"><a href="http://127.0.0.1:5199/" target="_blank" rel="noopener">Edge Services fixture</a><a href="http://127.0.0.1:5200/" target="_blank" rel="noopener">Agent run fixture</a><a href="http://127.0.0.1:5201/" target="_blank" rel="noopener">Deployment fixture</a></div>
  </div>
</template>

<style scoped>
.spacing-study__section { display: grid; gap: 16px; min-width: 0; }
.spacing-study__section h3 { margin: 0 0 6px; }
.spacing-study__shell { border: 1px dashed var(--color-border-default); min-width: 0; padding: 20px 16px; }
.spacing-study__gutter-label { color: var(--color-text-secondary); margin-bottom: 12px; }
.spacing-study__facts { display: grid; gap: 8px 24px; grid-template-columns: repeat(auto-fit, minmax(min(100%, 200px), 1fr)); margin: 0; }
.spacing-study__facts div { border-top: 1px solid var(--color-border-subtle); padding-top: 8px; }
.spacing-study__facts dt { font-weight: 600; }
.spacing-study__facts dd, .spacing-study__controls p { color: var(--color-text-secondary); margin: 4px 0 0; }
.spacing-study__controls { align-items: start; display: flex; flex-wrap: wrap; gap: 20px 32px; }
.spacing-study__controls > div { min-width: 0; }
.spacing-study__shapes { display: flex; flex-wrap: wrap; gap: 16px 24px; }
.spacing-study__shapes > div { display: grid; gap: 4px; justify-items: start; }
.spacing-study__shapes > div > span:last-child { color: var(--color-text-secondary); }
.spacing-study__shape { width: 48px; height: 48px; border: 1px solid var(--color-border-default); background: var(--color-surface-raised); }
.spacing-study__shape--tag { border-radius: var(--radius-sm); }
.spacing-study__shape--control { border-radius: var(--radius-md); }
.spacing-study__shape--panel { border-radius: var(--radius-lg); }
.spacing-study__shape--circle { border-radius: 50%; }
.spacing-study .spec-row a { color: var(--color-accent); text-decoration: underline; text-underline-offset: 3px; }
@media (min-width: 640px) { .spacing-study__shell { padding-inline: 32px; } }
</style>
