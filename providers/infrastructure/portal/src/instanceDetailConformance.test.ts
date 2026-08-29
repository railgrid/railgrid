import { describe, expect, it } from 'vitest'

const source = import.meta.glob('./views/InstanceDetailPage.vue', {
  query: '?raw',
  import: 'default',
  eager: true,
})['./views/InstanceDetailPage.vue'] as string

describe('Infrastructure instance detail conformance', () => {
  it('keeps route navigation outside the shared resource shell and guards deletion', () => {
    expect(source.indexOf('<ResourceBackLink')).toBeLessThan(source.indexOf('<ResourcePage'))
    expect(source).toContain('class="instance-detail__back"')
    expect(source).toContain(':disabled="deleting || deletionInProgress"')
    expect(source).toContain('@back="goBack"')
    expect(source).toContain('if (deleting.value || deletionInProgress.value) return')
  })

  it('retains the shared detail renderer and product-facing summary facts', () => {
    expect(source).toContain('kind="Instance"')
    expect(source).not.toMatch(/<ResourcePage[^>]*eyebrow=/)
    expect(source).not.toContain(':subtitle="inst?.template || \'Template instance\'"')
    expect(source).toContain('<span>Infrastructure</span>')
    expect(source).toContain('<span aria-hidden="true">·</span>')
    expect(source).toContain('<span v-if="inst">Template <code>{{ inst.template }}</code></span>')
    expect(source).toMatch(/<template v-if="inst" #status>/)
    expect(source).toContain('ResourceStatCards')
    expect(source).not.toContain("id: 'status'")
    expect(source).toContain('id: \'created\'')
    expect(source).toContain('label: \'Child resources\'')
    expect(source).toContain('.filter(({ value }) => !value.empty)')
    expect(source).toContain('.filter(group => group.fields.length > 0)')
    expect(source).toContain('<ViewValue :value="entry.value"')
    expect(source).not.toContain('id="instance-status"')
    expect(source).not.toContain('The infrastructure controller has not reported a status message.')
    expect(source).toContain(':title="group.title || \'\'"')
    expect(source).toContain('JSON.stringify(inst.values, null, 2)')
    expect(source).toContain("{ key: 'kind', label: 'Kind' }")
    expect(source).toContain("{ key: 'name', label: 'Name', primary: true }")
    expect(source).toContain("{ key: 'namespaceLabel', label: 'Namespace' }")
    expect(source).toContain("{ key: 'phaseLabel', label: 'Phase' }")
  })

  it('owns visually-hidden detail announcements in the standalone stylesheet', () => {
    expect(source).toContain('instance-detail__sr-only')
    expect(source).not.toContain('class="sr-only"')
  })

  it('keeps local deletion visible after the menu closes and uses the provider UI backlink', () => {
    expect(source).toContain('const deletionInProgress = computed(() => deleting.value || Boolean(inst.value && instanceIsDeleting(inst.value)))')
    expect(source).toContain('<p v-if="deleting" class="instance-message" role="status" aria-live="polite">Deleting this instance.')
    expect(source).toContain("actionsMenu.value?.removeAttribute('open')")
    expect(source).toContain('href="/ui/providers/infrastructure/instances"')
    expect(source).toContain("import ResourceBackLink from '../portalkit/ResourceBackLink.vue'")
  })

  it('keeps automatic refresh quiet while manual refresh remains foreground', () => {
    expect(source).toContain("load('background')")
    expect(source).toContain(':refresh-mode="refreshMode"')
    expect(source).toContain('const foregroundLoading = computed')
    expect(source).toContain("@click=\"load('foreground')\"")
    expect(source).not.toContain('window.setInterval')
  })
})
