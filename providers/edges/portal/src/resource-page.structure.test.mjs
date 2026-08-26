import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = (file) => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')
const resourcePage = readSource('portalkit/ResourcePage.vue')
const sectionCard = readSource('portalkit/ResourceSectionCard.vue')
const statCards = readSource('portalkit/ResourceStatCards.vue')
const farosUI = readSource('portalkit/faros-ui.css')
const detail = readSource('Detail.vue')
const app = readSource('App.vue')
const services = readSource('Services.vue')
const workloads = readSource('Workloads.vue')
const style = readSource('style.css')
const providerIcon = readFileSync(resolve(process.cwd(), 'public', 'icon.svg'), 'utf8')

function resourcePageBlock(source) {
  const start = source.indexOf('<ResourcePage')
  const end = source.indexOf('</ResourcePage>', start)
  return start >= 0 && end >= 0 ? source.slice(start, end + '</ResourcePage>'.length) : ''
}

function resourcePageOpening(source) {
  const start = source.indexOf('<ResourcePage')
  const end = source.indexOf('>', start)
  return start >= 0 && end >= 0 ? source.slice(start, end + 1) : ''
}

function resourcePageSlot(source, name) {
  const block = resourcePageBlock(source)
  const start = block.indexOf('<template #' + name + '>')
  const contentStart = start >= 0 ? start + ('<template #' + name + '>').length : -1
  const end = contentStart >= 0 ? block.indexOf('</template>', contentStart) : -1
  return contentStart >= 0 && end >= 0 ? block.slice(contentStart, end) : ''
}

describe('resource detail cards', () => {
  it('uses a network topology mark for the Edges provider', () => {
    expect(providerIcon).toMatch(/<rect x="9" y="2" width="6" height="6"/)
    expect(providerIcon).toMatch(/<rect x="2" y="16" width="6" height="6"/)
    expect(providerIcon).toMatch(/<rect x="16" y="16" width="6" height="6"/)
    expect(providerIcon).toContain('M5 16v-3a1 1 0 0 1 1-1h12')
    expect(providerIcon).not.toContain('<circle')
  })

  it('keeps the ResourcePage read-state contract additive', () => {
    expect(resourcePage).toMatch(/class="k-resource-page__summary"/)
    expect(resourcePage).toMatch(/class="k-resource-page__body"/)
    expect(resourcePage).toMatch(/class="k-resource-page__stale"/)
    const order = [
      resourcePage.indexOf('class="k-resource-page__stale"'),
      resourcePage.indexOf('class="k-resource-page__summary"'),
      resourcePage.indexOf('class="k-resource-page__body"'),
    ]
    expect(order.every(index => index >= 0)).toBe(true)
    expect(order).toEqual([...order].sort((a, b) => a - b))
  })

  it('preserves the backlink, title, and fixed Edge action order', () => {
    expect(detail).toMatch(/<div class="edge-detail">/)
    expect(detail).toMatch(/<a class="k-btn k-btn--ghost edge-detail__back" href="\/ui\/providers\/edges" @click\.prevent="emit\('back'\)"[^>]*>[\s\S]*<ArrowLeft[\s\S]*\/> Edges/)
    expect(detail).toMatch(/class="edge-detail__provider-mark"/)
    const page = resourcePageBlock(detail)
    const opening = resourcePageOpening(detail)
    const meta = resourcePageSlot(detail, 'meta')
    const status = resourcePageSlot(detail, 'status')
    expect(opening).toContain(':kind="edgeTypeLabel"')
    expect(opening).not.toContain('eyebrow=')
    expect(meta).toContain('<span>Edges</span>')
    expect(meta).toContain('edge.hostname')
    expect(meta).not.toContain('StatusBadge')
    expect(status).toContain('StatusBadge')
    const headerOrder = [
      page.indexOf(':kind="edgeTypeLabel"'),
      page.indexOf('<template #meta>'),
      page.indexOf('<template #status>'),
    ]
    expect(headerOrder.every(index => index >= 0)).toBe(true)
    expect(headerOrder).toEqual([...headerOrder].sort((a, b) => a - b))
    expect(detail).toMatch(/<template #actions>[\s\S]*Open terminal[\s\S]*Refresh[\s\S]*More edge actions[\s\S]*Delete/)
    expect(detail).not.toMatch(/:breadcrumbs=/)
    expect(detail).not.toMatch(/edge-overview|edge-detail-section|edge-section-summary|Resource details/)
  })

  it('uses six shared provider-owned icon stat cards for both edge kinds', () => {
    expect(sectionCard).toMatch(/class="k-resource-section-card"/)
    expect(sectionCard).toMatch(/class="k-resource-section-card__actions"/)
    expect(sectionCard).toMatch(/class="k-resource-section-card__body"/)
    expect(statCards).toMatch(/interface ResourceStatCard/)
    expect(farosUI).toMatch(/\.k-resource-stat-cards\s*\{[\s\S]*grid-template-columns: repeat\(3/)
    expect(farosUI).toMatch(/@media \(max-width: 859px\)[\s\S]*\.k-resource-stat-cards[\s\S]*repeat\(2/)
    expect(farosUI).toMatch(/@media \(max-width: 520px\)[\s\S]*\.k-resource-stat-cards[\s\S]*minmax\(0, 1fr\)/)
    expect(detail).toMatch(/import ResourceStatCards, \{ type ResourceStatCard \}/)
    expect(detail).toMatch(/import ResourceSectionCard from '.\/portalkit\/ResourceSectionCard\.vue'/)
    expect(detail).toMatch(/const edgeStatCards = computed<ResourceStatCard\[\]>\(\(\) => \[/)
    expect(detail).toMatch(/id: 'connection'[\s\S]*id: 'provider'[\s\S]*id: 'type'[\s\S]*id: 'hostname'[\s\S]*id: 'agent'[\s\S]*id: 'services'/)
    expect(detail).toMatch(/id: 'provider'[\s\S]*value: 'Edges'/)
    expect(detail).toMatch(/const servicesLoaded = ref\(false\)/)
    expect(detail).toMatch(/const servicesCardValue = computed\(\(\) => servicesLoaded\.value \? String\(services\.value\.length\) : '—'\)/)
    expect(detail).toMatch(/<template #summary>[\s\S]*<ResourceStatCards :cards="edgeStatCards" aria-label="Edge summary" \/>/)
    expect(detail).toMatch(/<ResourceSectionCard id="edge-connectivity"[\s\S]*title="Connectivity"/)
    expect(detail).toMatch(/<ResourceSectionCard id="edge-services"[\s\S]*title="Services"/)
    expect(detail).toMatch(/<ResourceSectionCard id="edge-technical"[\s\S]*title="Technical details"/)
    expect(style).toMatch(/\.edge-detail__sections\s*\{[\s\S]*gap:/)
  })

  it('keeps edge operations and progressive disclosures accessible', () => {
    expect(detail).toMatch(/:aria-expanded="showUpgrade"[\s\S]*aria-controls="edges-upgrade-commands"/)
    expect(detail).toMatch(/<details v-if="!edge\.connected && edge\.joinToken" class="edge-disclosure">/)
    expect(detail).toMatch(/<details v-if="type === 'kubernetes' && edge\.connected" class="edge-disclosure">/)
    expect(detail).toMatch(/<details v-if="type === 'server' && edge\.connected" class="edge-disclosure">/)
    expect(detail).toMatch(/const servicesExpanded = ref\(false\)/)
    expect(detail).toMatch(/:aria-expanded="servicesExpanded"[\s\S]*aria-controls="edges-services-content"/)
    expect(detail).toMatch(/v-if="edge && servicesExpanded"[\s\S]*id="edges-services-content"/)
    expect(detail).toMatch(/const technicalExpanded = ref\(false\)/)
    expect(detail).toMatch(/:aria-expanded="technicalExpanded"[\s\S]*aria-controls="edges-technical-content"/)
    expect(detail).toMatch(/v-if="edge && technicalExpanded"[\s\S]*id="edges-technical-content"[\s\S]*metadataRows/)
    expect(detail).not.toMatch(/joinToken.*k-resource-technical|k-resource-technical[\s\S]*joinToken/)
    expect(style).toMatch(/\.edge-disclosure\s*\{[\s\S]*border:/)
    expect(style).toMatch(/@media \(max-width: 520px\)/)
  })

  it('uses quiet adaptive refresh across edge resource views', () => {
    for (const source of [app, detail, services, workloads]) {
      expect(source).toMatch(/createAdaptiveRefreshTimer/)
      expect(source).toMatch(/createLatestRefreshController/)
      expect(source).toMatch(/'background'/)
      expect(source).toMatch(/:refresh-mode="refreshMode"/)
      expect(source).not.toMatch(/setInterval\(/)
    }
    expect(detail).toMatch(/detailRefreshing = computed\(\(\) => loading\.value && refreshMode\.value === 'foreground'\)/)
    expect(app).toMatch(/foregroundLoading = computed\(\(\) => loading\.value && refreshMode\.value === 'foreground'\)/)
    expect(app).toMatch(/authorityChanged = !previous \|\| tenant !== previous\[1\] \|\| userSub !== previous\[2\]/)
    expect(app).toMatch(/Token rotation within the same user\/workspace[\s\S]*refresh\('background'\)/)
  })
})
