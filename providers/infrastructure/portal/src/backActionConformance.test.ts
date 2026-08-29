import { describe, expect, it } from 'vitest'

const sources = import.meta.glob('./views/{ProvisionPage,MissingCredentialsPage,InstanceDetailPage}.vue', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>
const resourceBackLink = import.meta.glob('./portalkit/ResourceBackLink.vue', {
  query: '?raw',
  import: 'default',
  eager: true,
})['./portalkit/ResourceBackLink.vue'] as string

describe('Infrastructure back-navigation conformance', () => {
  it('keeps page-level back actions on the shared recipe', () => {
    expect(Object.keys(sources)).toHaveLength(3)
    expect(sources['./views/ProvisionPage.vue']).toContain('k-btn k-btn--ghost k-back-action')
    expect(sources['./views/MissingCredentialsPage.vue']).toContain('k-btn k-btn--ghost k-back-action')

    const instanceDetail = sources['./views/InstanceDetailPage.vue']
    expect(instanceDetail).toContain("import ResourceBackLink from '../portalkit/ResourceBackLink.vue'")
    expect(instanceDetail).toContain('<ResourceBackLink')
    expect(instanceDetail).toContain('href="/ui/providers/infrastructure/instances"')
    expect(instanceDetail).toContain(':disabled="deleting || deletionInProgress"')
    expect(instanceDetail).toContain('@back="goBack"')
    expect(resourceBackLink).toContain('class="k-btn k-btn--ghost k-back-action"')
  })
})
