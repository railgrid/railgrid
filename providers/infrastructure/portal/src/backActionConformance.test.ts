import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ResourceBackLink from './portalkit/ResourceBackLink.vue'

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
    expect(resourceBackLink).toContain(":class=\"['k-btn k-btn--ghost k-back-action', { 'k-back-action--icon-only': iconOnly }]\"")
    expect(resourceBackLink).toContain('iconOnly?: boolean')
    expect(resourceBackLink).toContain('iconOnly: false')
    expect(resourceBackLink).toContain('<slot v-if="!iconOnly">Back</slot>')
  })

  it('keeps the default backlink labeled and names icon-only links', async () => {
    const href = '/ui/providers/infrastructure/instances'
    const defaultHTML = await renderToString(createSSRApp(ResourceBackLink, { href }))
    expect(defaultHTML).toMatch(/<a[^>]*class="k-btn k-btn--ghost k-back-action"[^>]*>/)
    expect(defaultHTML).toMatch(/Back(?:<!--\]-->)?<\/a>/)

    const iconOnlyHTML = await renderToString(createSSRApp(ResourceBackLink, {
      href,
      iconOnly: true,
      'aria-label': 'Back to instances',
    }))
    expect(iconOnlyHTML).toContain('k-back-action--icon-only')
    expect(iconOnlyHTML).toContain('aria-label="Back to instances"')
    expect(iconOnlyHTML).not.toContain('>Back</a>')
    expect(iconOnlyHTML).toMatch(/<svg[^>]*aria-hidden="true"/)
  })
})
