import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, onTestFinished } from 'vitest'
import AISkillCatalogList from './agentkit/AISkillCatalogList.vue'

it('explains no search results and returns focus to search when cleared', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const query = ref('  missing  ')
  const app = createApp({ render: () => h(AISkillCatalogList, {
    skills: [{ id: 'review', name: 'Review', description: 'Review the rollout.', scope: 'workspace' }], query: query.value,
    'onUpdate:query': (value: string) => { query.value = value },
  }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })

  const status = () => host.querySelector('.k-ai-skill-catalog__region [role="status"]')!
  expect(status().textContent).toContain('No skills match “missing”. Clear the search to see all skills.')
  const longQuery = 'x'.repeat(500)
  query.value = longQuery
  await nextTick()
  expect(status().textContent).toContain(`${'x'.repeat(80)}…`)
  expect(status().textContent).not.toContain(longQuery)
  expect(query.value).toBe(longQuery)
  expect(status().querySelector('.skill-catalog-empty-text')).not.toBeNull()
  host.querySelector<HTMLButtonElement>('[aria-label="Clear skill search"]')!.click()
  await nextTick()
  expect(query.value).toBe('')
  expect(document.activeElement).toBe(host.querySelector('input[type="search"]'))
  expect(host.querySelector('li')?.textContent).toContain('Review')
  query.value = '   '
  await nextTick()
  expect(host.querySelector('li')?.textContent).toContain('Review')
})
