import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = (file) => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')
const styles = readSource('style.css')
const serviceEdit = readSource('ServiceEdit.vue')

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

describe('Service detail narrow-screen regressions', () => {
  it('contains the resource composition and collapses facts before the viewport can overflow', () => {
    expect(styles).toMatch(/\.service-detail\s*\{[\s\S]*max-width:\s*100%;/)
    expect(styles).toMatch(/\.service-detail__resource\s*\{[\s\S]*width:\s*100%;[\s\S]*max-width:\s*100%;/)
    expect(styles).toMatch(/\.service-detail__resource,[\s\S]*\.service-detail__card\s*>\s*\*\s*>\s*\*\s*,[\s\S]*box-sizing:\s*border-box;[\s\S]*min-width:\s*0;/)
    expect(styles).toMatch(/\.service-detail__page > header > :first-child,[\s\S]*overflow-wrap:\s*anywhere;[\s\S]*word-break:\s*break-word;/)
    expect(styles).toMatch(/@media\s*\(max-width:\s*420px\)[\s\S]*\.service-detail__facts\s*\{\s*grid-template-columns:\s*minmax\(0,\s*1fr\);/)
  })

  it('separates unsupported, optional, and required credential states', () => {
    expect(serviceEdit).toMatch(/const credentialsSupported = computed\(\(\) => \{[\s\S]*auth !== '' && auth !== 'none'/)
    expect(serviceEdit).toMatch(/const credentialsOptional = computed\(\(\) => credentialsSupported\.value && !!entry\.value\?\.credential\.optional\)/)
    expect(serviceEdit).toMatch(/const credentialsRequired = computed\(\(\) => credentialsSupported\.value && !credentialsOptional\.value\)/)
    expect(serviceEdit).toMatch(/value: 'Not configured \(optional\)'[\s\S]*tone: 'default'/)
    expect(serviceEdit).toMatch(/value: 'Missing'[\s\S]*tone: 'warning'/)
    expect(serviceEdit).toMatch(/No credentials required for this service\./)
    expect(serviceEdit).toMatch(/<template v-if="credentialsSupported">/)
    expect(serviceEdit).toMatch(/credentialsOptional \? ' \(optional\)'/)
  })

  it('summarizes credential health once while retaining the detailed credentials section', () => {
    expect(serviceEdit).not.toMatch(/id: 'credentials'/)
    expect(serviceEdit).toMatch(/id: 'status'[\s\S]*detail: credentialState\.value\.detail/)
    expect(serviceEdit).toMatch(/<ResourceSectionCard[^>]*id="service-credentials"[^>]*eyebrow="Access"[^>]*title="Credentials"/)
  })

  it('uses the shared Service kind, meta, status, and subtitle header contract', () => {
    const page = resourcePageBlock(serviceEdit)
    const opening = resourcePageOpening(serviceEdit)
    const meta = resourcePageSlot(serviceEdit, 'meta')
    const status = resourcePageSlot(serviceEdit, 'status')
    expect(opening).toContain('kind="Service"')
    expect(opening).not.toContain('eyebrow=')
    expect(opening).toMatch(/:subtitle="entry\?\.displayName \|\| service\?\.serviceType \|\| 'Edge service'"/)
    expect(meta).toMatch(/<span>Edges<\/span>[\s\S]*service\?\.edgeName/)
    expect(status).toContain('StatusBadge')
    const headerOrder = [
      page.indexOf('kind="Service"'),
      page.indexOf('<template #meta>'),
      page.indexOf('<template #status>'),
    ]
    expect(headerOrder.every(index => index >= 0)).toBe(true)
    expect(headerOrder).toEqual([...headerOrder].sort((a, b) => a - b))
  })

  it('keeps deletion status visible and politely announced after the menu closes', () => {
    expect(serviceEdit).toMatch(/const deleting = ref\(false\)/)
    expect(serviceEdit).toMatch(/if \(deleting\.value\) return 'Deleting'/)
    expect(serviceEdit).toMatch(/<p v-if="deleting" class="waiting" role="status" aria-live="polite">Deleting this service\./)
    expect(serviceEdit).toMatch(/import ActionMenu, \{ type ActionMenuItem \} from '\.\/portalkit\/ActionMenu\.vue'/)
    expect(serviceEdit).toContain("busy: deleting.value")
    expect(serviceEdit).toMatch(/<ActionMenu[\s\S]*label="More service actions"[\s\S]*@select="selectAction"/)
    expect(serviceEdit).toMatch(/deleting\.value = true[\s\S]*await deleteEdgeService\(name\)[\s\S]*emit\('deleted'\)/)
    expect(serviceEdit).toMatch(/mutationError\.value = errorMessage\(error, 'Delete failed'\)[\s\S]*deleting\.value = false/)
  })

  it('uses the provider UI route as the service backlink fallback', () => {
    expect(serviceEdit).toMatch(/<ResourceBackLink class="service-detail__back" href="\/ui\/providers\/edges\/services" @back="emit\('back'\)">[\s\S]*Services[\s\S]*<\/ResourceBackLink>/)
    expect(serviceEdit).toMatch(/import ResourceBackLink from '\.\/portalkit\/ResourceBackLink\.vue'/)
  })
})
