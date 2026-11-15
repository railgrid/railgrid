import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { expect, it, vi } from 'vitest'
import OrgsSection from '../../portal/src/pages/bonkers/OrgsSection.vue'
import { useAdminStore } from '../../portal/src/stores/admin'

it('shows exact recovery deadlines and handles invalid values in both admin detail views', async () => {
  const pinia = createPinia()
  setActivePinia(pinia)
  const admin = useAdminStore()
  const deadline = '2026-11-01T06:30:00Z'
  admin.orgs = [{ name: 'north', displayName: 'North', workspacePath: 'root:north', deletionRequestedAt: '2026-10-29T06:30:00Z', purgeAt: deadline, workspaces: [{ uuid: 'staging', displayName: 'Staging', clusterName: 'fixture', providers: [], deletionRequestedAt: '2026-10-29T06:30:00Z', purgeAt: deadline }] }]
  admin.loaded = true
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: OrgsSection }] })
  await router.push({ path: '/', query: { organization: 'north' } })
  await router.isReady()
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(OrgsSection) }).use(pinia).use(router)
  app.mount(root)
  await nextTick()
  const orgTime = root.querySelector<HTMLTimeElement>('time')!
  expect(orgTime.dateTime).toBe(deadline)
  expect(orgTime.textContent).toMatch(/2026/)
  expect(orgTime.textContent).toMatch(/\b(?:UTC|GMT|[A-Z]{2,5})\b/)
  await router.push({ path: '/', query: { organization: 'north', workspace: 'staging' } })
  await vi.waitFor(() => expect(root.querySelector('#workspace-identity time')).not.toBeNull())
  const workspaceTime = root.querySelector<HTMLTimeElement>('#workspace-identity time')!
  expect(workspaceTime.dateTime).toBe(deadline)
  admin.orgs[0]!.workspaces[0]!.purgeAt = 'invalid'
  await nextTick()
  expect(root.querySelector('#workspace-identity')?.textContent).toContain('Deadline unavailable')
  expect(root.querySelector('#workspace-identity time')).toBeNull()
  await router.push({ path: '/', query: { organization: 'north' } })
  admin.orgs[0]!.purgeAt = 'invalid'
  await nextTick()
  expect(root.textContent).toContain('Recovery deadline unavailable.')
  expect(root.textContent).not.toContain('not-a-timestamp')
  app.unmount(); root.remove()
})
