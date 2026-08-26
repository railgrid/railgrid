import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = (file) => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')
const app = readSource('App.vue')
const detail = readSource('views/RepoDetailView.vue')
const connectionDetail = readSource('views/ConnectionDetailView.vue')
const style = readSource('style.css')
const sectionCard = readSource('portalkit/ResourceSectionCard.vue')
const statCards = readSource('portalkit/ResourceStatCards.vue')
const farosUI = readSource('portalkit/faros-ui.css')

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

describe('Code repository resource detail cards', () => {
  it('hides provider tabs for repository detail and preserves the backlink', () => {
    expect(app).toMatch(/<template v-if="!route\.repo && !route\.connection">[\s\S]*<Tabs :tabs=/)
    expect(detail).toMatch(/<a class="k-btn k-btn--ghost repo-detail__back" href="\/ui\/providers\/code\/repositories" @click\.prevent="emit\('back'\)"[^>]*>[\s\S]*<ArrowLeft/)
    expect(detail).not.toMatch(/:breadcrumbs=|@navigate=|<Tabs\b/)
  })

  it('keeps the fixed header action order and current read/delete behavior', () => {
    expect(detail).toMatch(/const repositoryRefreshing = computed\(\(\) => repoLoading\.value\)/)
    const repositoryActionBusy = detail.match(/const repositoryActionBusy = computed\([^\n]+/)?.[0] ?? ''
    expect(repositoryActionBusy).not.toMatch(/connectionsLoading|keysLoading|collabsLoading|packagesLoading/)
    const page = resourcePageBlock(detail)
    const opening = resourcePageOpening(detail)
    const meta = resourcePageSlot(detail, 'meta')
    const status = resourcePageSlot(detail, 'status')
    expect(opening).toContain('kind="Repository"')
    expect(opening).not.toContain('eyebrow=')
    expect(meta).toContain('providerLabel(currentConn?.provider)')
    expect(meta).not.toContain('Repository')
    expect(meta).not.toContain('StatusBadge')
    expect(status).toContain('StatusBadge')
    const headerOrder = [
      page.indexOf('kind="Repository"'),
      page.indexOf('<template #meta>'),
      page.indexOf('<template #status>'),
    ]
    expect(headerOrder.every(index => index >= 0)).toBe(true)
    expect(headerOrder).toEqual([...headerOrder].sort((a, b) => a - b))
    expect(detail).toMatch(/const repositoryTitle = computed\([\s\S]*repositoryOwner\.value \? `\$\{repositoryOwner\.value\}\/\$\{repositoryName\}`/)
    expect(detail).toMatch(/:title="repositoryTitle"/)
    expect(detail).toMatch(/<div class="repo-detail__provider-mark"[\s\S]*<Github v-if="isGitHubProvider"[\s\S]*<GitBranch v-else/)
    expect(detail).toMatch(/<div class="repo-detail__actions" role="group" aria-label="Repository actions">/)
    expect(detail).toMatch(/Open repository[\s\S]*k-btn--primary/)
    expect(detail).toMatch(/class="k-btn k-btn--ghost"[\s\S]*@click="loadAll"/)
    expect(detail).toMatch(/<details ref="actionsMenu" class="repo-detail__menu">[\s\S]*Delete repository/)
    expect(detail).toMatch(/@retry="loadRepository"/)
    expect(detail).toMatch(/async function deleteRepository\(\)/)
    expect(detail).toMatch(/confirmDialog\(\{[\s\S]*danger: true/)
    expect(detail).toMatch(/api\.deleteRepository\(current\.name\)/)
    expect(detail).toMatch(/props\.deletions\.acknowledge\(repositoryScope, current\.name, current\.uid\)/)
    expect(detail).toMatch(/const repositoryDeleteInFlight = computed\(\(\) => operations\.phase\(operationKey\('repository', props\.name\)\) === 'deleting'\)/)
    expect(detail).toMatch(/<p v-if="repositoryDeleting" class="repo-detail__deleting" role="status" aria-live="polite">[\s\S]*Deleting this repository\./)
  })

  it('keeps overflow action menus wider than their icon triggers', () => {
    expect(style).toMatch(/\.repo-detail \.repo-detail__menu-popover\s*\{[\s\S]*width:\s*max-content;[\s\S]*min-width:\s*170px;[\s\S]*max-width:\s*min\(240px, calc\(100vw - 32px\)\)/)
    expect(style).toMatch(/\.repo-detail__menu-item\s*\{[\s\S]*width:\s*100%;[\s\S]*white-space:\s*nowrap/)
    expect(style).toMatch(/\.connection-detail \.connection-detail__menu-popover\s*\{[\s\S]*width:\s*max-content;[\s\S]*min-width:\s*170px;[\s\S]*max-width:\s*min\(240px, calc\(100vw - 32px\)\)/)
    expect(style).toMatch(/\.connection-detail__menu-item\s*\{[\s\S]*width:\s*100%;[\s\S]*white-space:\s*nowrap/)
  })

  it('uses compact provider-owned stat cards and canonical section cards', () => {
    expect(sectionCard).toMatch(/class="k-resource-section-card"/)
    expect(sectionCard).toMatch(/class="k-resource-section-card__actions"/)
    expect(sectionCard).toMatch(/class="k-resource-section-card__body"/)
    expect(statCards).toMatch(/interface ResourceStatCard/)
    expect(farosUI).toMatch(/\.k-resource-stat-cards\s*\{[\s\S]*grid-template-columns: repeat\(3/)
    expect(farosUI).toMatch(/\.k-resource-stat-cards--compact \.k-resource-stat-card\s*\{[\s\S]*min-height:/)
    expect(detail).toMatch(/import ResourceStatCards, \{ type ResourceStatCard \}/)
    expect(detail).toMatch(/import ResourceSectionCard from '..\/portalkit\/ResourceSectionCard\.vue'/)
    expect(detail).toMatch(/const repositoryStatCards = computed<ResourceStatCard\[\]>\(\(\) => \[/)
    expect(detail).toMatch(/<template #summary>[\s\S]*<ResourceStatCards :cards="repositoryStatCards" density="compact" aria-label="Repository summary" \/>/)
    expect(detail).toMatch(/id: 'integration'[\s\S]*id: 'provider'[\s\S]*id: 'type'[\s\S]*id: 'owner'[\s\S]*id: 'default-branch'[\s\S]*id: 'visibility'/)
    expect(detail).not.toMatch(/repo-overview|repository-overview|repo-detail-section|repo-section-summary|Resource details/)
    expect(detail).toMatch(/<ResourceSectionCard\s+id="repository-integration"[\s\S]*repo-integration-card__description/)
    expect(detail).toMatch(/<ResourceSectionCard id="repository-access"[\s\S]*#actions/)
    expect(detail).toMatch(/<ResourceSectionCard id="repository-packages"[\s\S]*#actions/)
    expect(detail).toMatch(/<ResourceSectionCard id="repository-conditions"[\s\S]*title="Health"/)
    expect(style).toMatch(/\.repo-detail__sections\s*\{[\s\S]*gap:/)
    expect(detail).toMatch(/<div v-if="connectionExpanded" id="repository-integration-editor" class="repo-integration-editor">/)
    expect(detail).toMatch(/<div v-if="accessExpanded" id="repository-access-content" class="grid-2">/)
    expect(detail).toMatch(/<div v-if="repo && packagesExpanded" id="repository-packages-content" class="repo-domain-block">/)
    expect(detail).not.toMatch(/technicalExpanded|repository-technical|repo-technical/)
  })

  it('keeps repository health in an always-visible conditions card', () => {
    const integration = detail.match(/<ResourceSectionCard\s+id="repository-integration"[\s\S]*?<\/ResourceSectionCard>/)?.[0] ?? ''
    const conditions = detail.match(/<ResourceSectionCard\s+id="repository-conditions"[\s\S]*?<\/ResourceSectionCard>/)?.[0] ?? ''
    expect(integration).toMatch(/<p class="repo-integration-card__description">/)
    expect(integration).toMatch(/class="repo-integration-card__connection"/)
    expect(integration).toMatch(/integrationHealth/)
    expect(integration).toMatch(/:aria-expanded="connectionExpanded" aria-controls="repository-integration-editor"[\s\S]*Change/)
    expect(integration).not.toMatch(/API version|Generation|Labels|Clone URL|SSH URL|Faros ID/)
    expect(conditions).toMatch(/<ConditionsPanel[\s\S]*:conditions="repo\?\.conditions \|\| \[\]"/)
    expect(conditions).toMatch(/Provider status[\s\S]*Repository ID[\s\S]*Browser URL[\s\S]*Clone URL[\s\S]*SSH URL/)
    expect(detail).toMatch(/repo\?\.htmlURL \|\| '—'[\s\S]*repo\?\.cloneURL \|\| '—'[\s\S]*repo\?\.sshURL \|\| '—'/)
    expect(detail).not.toMatch(/configurationRows|metadataRows|repositoryYaml|toYaml|YAML \/ read-only object|Technical details|technicalExpanded/)
    expect(detail).not.toMatch(/secretRef|privateKey|token/i)
    expect(detail).toMatch(/<div class="repo-section-card__facts" aria-label="Access counts">[\s\S]*deploy keys[\s\S]*collaborators/)
    expect(detail).toMatch(/<div class="repo-section-card__facts" aria-label="Package summary">[\s\S]*visible[\s\S]*packagesSummaryStatus/)
    expect(detail).toMatch(/<ArrowLeftRight[\s\S]*Change/)
    expect(detail).toMatch(/<Users[\s\S]*Manage access/)
    expect(detail).toMatch(/<PackageOpen[\s\S]*View packages/)
  })

  it('keeps repository resource tables wide inside their scroll regions', () => {
    const tableRule = farosUI.match(/\.k-table\.k-table--resource \.k-table__scroll > \.k-table__table\s*\{([\s\S]*?)\}/)?.[1] ?? ''
    expect(tableRule).toMatch(/max-width:\s*none/)
    expect(tableRule).toMatch(/min-width:\s*100%/)
    expect(tableRule).toMatch(/width:\s*max-content/)
    expect(farosUI).toMatch(/\.k-table__scroll\s*\{[\s\S]*overflow-x:\s*auto/)
    expect(farosUI).not.toMatch(/table-layout:\s*fixed/)
    expect(style).not.toMatch(/\.k-table(?:-[A-Za-z0-9_-]+)?\b/)
  })
})

describe('Code connection resource detail cards', () => {
  it('preserves the connection route backlink outside ResourcePage', () => {
    expect(app).toMatch(/connections<.*ConnectionDetailView|ConnectionDetailView.*connections/)
    expect(app).toMatch(/<template v-if="!route\.repo && !route\.connection">[\s\S]*<Tabs :tabs=/)
    expect(connectionDetail).toMatch(/<a class="k-btn k-btn--ghost connection-detail__back" href="\/ui\/providers\/code\/connections" @click\.prevent="emit\('back'\)"[^>]*>[\s\S]*<ArrowLeft[\s\S]*Connections/)
    expect(connectionDetail).not.toMatch(/:breadcrumbs=|@navigate=|<Tabs\b/)
  })

  it('keeps the shared read contract and Refresh-before-overflow Delete order', () => {
    expect(connectionDetail).toMatch(/<ResourcePage[\s\S]*:title="conn\?\.name \|\| name"/)
    const page = resourcePageBlock(connectionDetail)
    const opening = resourcePageOpening(connectionDetail)
    const meta = resourcePageSlot(connectionDetail, 'meta')
    const status = resourcePageSlot(connectionDetail, 'status')
    expect(opening).toContain('kind="Connection"')
    expect(opening).not.toContain('eyebrow=')
    expect(meta).toMatch(/conn\?\.provider[\s\S]*connection-header__separator[\s\S]*conn\?\.type[\s\S]*connection-header__separator[\s\S]*conn\.login/)
    expect(meta).not.toContain('<span>Connection</span>')
    expect(meta).not.toContain('StatusBadge')
    expect(status).toContain('StatusBadge')
    const headerOrder = [
      page.indexOf('kind="Connection"'),
      page.indexOf('<template #meta>'),
      page.indexOf('<template #status>'),
    ]
    expect(headerOrder).toEqual([...headerOrder].sort((a, b) => a - b))
    expect(connectionDetail).toMatch(/<template #actions>[\s\S]*Refresh[\s\S]*More connection actions[\s\S]*Delete connection/)
    expect(connectionDetail).toContain(':loaded="connectionReadState"')
    expect(connectionDetail).toContain(':stale="loaded && !!error"')
    expect(connectionDetail).toContain('@retry="load"')
    expect(connectionDetail).toMatch(/const connectionReadState = computed<boolean \| null>/)
    expect(connectionDetail).toMatch(/const detailRefreshing = computed\(\(\) => loading\.value\)/)
    expect(connectionDetail).toMatch(/const connectionDeleteInFlight = computed\(\(\) => operations\.phase\(operationKey\('connection', conn\.value\?\.name \|\| props\.name\)\) === 'deleting'\)/)
    expect(connectionDetail).toMatch(/const deleting = computed\(\(\) => !!conn\.value && \([\s\S]*connectionDeleteInFlight\.value/)
    expect(connectionDetail).toMatch(/createAdaptiveRefreshTimer/)
    expect(connectionDetail).toMatch(/FAST_REFRESH_MS/)
    expect(connectionDetail).toMatch(/createLatestRefreshController/)
    expect(connectionDetail).toContain(':stale="loaded && !!error"')
    expect(connectionDetail).toContain('Updating connection…')
    expect(connectionDetail).toContain('mutationError')
    expect(connectionDetail).toContain('Deleting this connection.')
    expect(connectionDetail).toMatch(/async function deleteConnection\(\)/)
    expect(connectionDetail).toMatch(/confirmDialog\(\{[\s\S]*danger: true/)
    expect(connectionDetail).toMatch(/api\.deleteConnection\(current\.name\)/)
    expect(connectionDetail).toMatch(/props\.deletions\.acknowledge\(deletionScope, current\.name, current\.uid\)/)
    expect(connectionDetail).toMatch(/err\.reason === 'NotFound'[\s\S]*props\.deletions\.has\(deletionScope, props\.name\)/)
  })

  it('uses compact stat cards and keeps overview, credential, and health sections', () => {
    expect(connectionDetail).toMatch(/<div class="connection-detail">/)
    expect(connectionDetail).toMatch(/import ResourceStatCards, \{ type ResourceStatCard \}/)
    expect(connectionDetail).toMatch(/import ResourceSectionCard from '..\/portalkit\/ResourceSectionCard\.vue'/)
    expect(connectionDetail).toMatch(/const connectionStatCards = computed<ResourceStatCard\[\]>\(\(\) => \{/)
    expect(connectionDetail).toMatch(/<ResourceStatCards :cards="connectionStatCards" density="compact" aria-label="Connection summary" \/>/)
    expect(connectionDetail).toMatch(/id: 'connection'[\s\S]*id: 'provider'[\s\S]*id: 'type'[\s\S]*id: 'owner'/)
    expect(connectionDetail).toMatch(/if \(login\) cards\.push\(\{ id: 'login'/)
    expect(connectionDetail).toMatch(/if \(conn\.value\?\.scopes\.length\)[\s\S]*id: 'scopes'/)
    expect(connectionDetail).toMatch(/<dt v-if="conn\.login">Login<\/dt><dd v-if="conn\.login">/)
    expect(connectionDetail).toMatch(/<dt v-if="conn\.scopes\.length">Scopes<\/dt>[\s\S]*<dd v-if="conn\.scopes\.length">/)
    expect(connectionDetail).toMatch(/<ResourceSectionCard id="connection-overview"[\s\S]*title="Overview"/)
    expect(connectionDetail).toMatch(/<ResourceSectionCard id="connection-credentials"[\s\S]*title="Credential reference"/)
    expect(connectionDetail).toMatch(/<ResourceSectionCard id="connection-conditions"[\s\S]*title="Health"[\s\S]*ConditionsPanel/)
    expect(connectionDetail).toMatch(/secretName[\s\S]*secretNamespace[\s\S]*secretKey/)
    expect(connectionDetail).toMatch(/baseURL[\s\S]*observedGeneration[\s\S]*generation/)
    expect(connectionDetail).toMatch(/CredentialUnavailable[\s\S]*ValidationFailed[\s\S]*ProviderNotFound/)
    expect(connectionDetail).not.toMatch(/conn\.token|credentialToken|secretValue|privateKey/i)
    expect(style).toMatch(/\.connection-detail__sections\s*\{[\s\S]*gap:/)
  })
})
