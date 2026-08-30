import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-publishing-state', configFile: false, server: { middlewareMode: true } })
const {
  productionAccessState,
  productionDeploymentDescription,
  productionDeploymentState,
  publishedApplicationURL,
  publishingAccessPresentation,
  publishingAccessSelection,
  shouldPollPublishing,
} = await vite.ssrLoadModule('/src/publishingState.ts')
test.after(async () => vite.close())

test('defaults unpublished and malformed publications to invite-only', () => {
  assert.equal(publishingAccessSelection({ published: false }), 'members')
  assert.equal(publishingAccessSelection({ published: true }), 'members')
  assert.equal(publishingAccessSelection({ published: true, publication: { mode: null } }), 'members')
})

test('preserves public selection only for an active public publication', () => {
  assert.equal(publishingAccessSelection({ published: true, publication: { mode: 'public' } }), 'public')
  assert.equal(publishingAccessSelection({ published: true, publication: { mode: 'restricted' } }), 'members')
})

test('uses only the platform-published URL for production access', () => {
  assert.equal(publishedApplicationURL(null), '')
  assert.equal(publishedApplicationURL({ published: false, publication: { url: 'https://stale.example.test' } }), '')
  assert.equal(publishedApplicationURL({ published: true, publication: { url: ' https://app.example.test ' } }), 'https://app.example.test')
})

test('polls while a publication is converging and stops when URL and readiness agree', () => {
  assert.equal(shouldPollPublishing({ published: false }), false)
  assert.equal(shouldPollPublishing({ published: true }), true)
  assert.equal(shouldPollPublishing({ published: true, publication: { ready: true } }), true)
  assert.equal(shouldPollPublishing({ published: true, publication: { ready: false, url: 'https://app.example.test' } }), true)
  assert.equal(shouldPollPublishing({ published: true, publication: { ready: true, url: 'https://app.example.test' } }), false)
})

test('presents unknown, disabled, enabling, and enabled access without guessing', () => {
  assert.deepEqual(publishingAccessPresentation(null), {
    label: 'Checking',
    tone: 'muted',
    description: 'Checking the current external access policy…',
    loading: true,
  })
  assert.deepEqual(publishingAccessPresentation({ published: false }), {
    label: 'Disabled',
    tone: 'muted',
    description: 'Use Share to choose who can open the production app.',
    loading: false,
  })
  assert.deepEqual(publishingAccessPresentation({ published: true }), {
    label: 'Enabling',
    tone: 'warning',
    description: 'External access is being enabled for this production deployment.',
    loading: false,
  })
  assert.deepEqual(publishingAccessPresentation({
    published: true,
    publication: { ready: true, url: 'https://app.example.test' },
  }), {
    label: 'Enabled',
    tone: 'success',
    description: 'External access is enabled for this production deployment.',
    loading: false,
  })
})

test('keeps an absent production binding out of the deployed state', () => {
  assert.deepEqual(productionDeploymentState(null), {
    deployed: false,
    ready: false,
    label: 'Offline',
    tone: 'muted',
  })
})

test('reports the binding phase until production is Ready', () => {
  assert.deepEqual(productionDeploymentState({ phase: 'Provisioning' }), {
    deployed: true,
    ready: false,
    label: 'Provisioning',
    tone: 'warning',
  })
  assert.deepEqual(productionDeploymentState({ phase: 'Failed' }), {
    deployed: true,
    ready: false,
    label: 'Failed',
    tone: 'danger',
  })
  assert.deepEqual(productionDeploymentState({ phase: 'Ready' }), {
    deployed: true,
    ready: true,
    label: 'Ready',
    tone: 'success',
  })
})

test('separates production readiness from external access', () => {
  const readyBinding = { phase: 'Ready' }
  const publishing = { published: true, publication: { ready: false, url: 'https://app.example.test' } }

  assert.deepEqual(productionAccessState(readyBinding, null), { label: 'Offline', tone: 'muted', url: '' })
  assert.deepEqual(productionAccessState(readyBinding, { published: false }), { label: 'Offline', tone: 'muted', url: '' })
  assert.deepEqual(productionAccessState({ phase: 'Provisioning' }, publishing), { label: 'Offline', tone: 'muted', url: '' })
  assert.deepEqual(productionAccessState(readyBinding, publishing), {
    label: 'Publishing',
    tone: 'warning',
    url: 'https://app.example.test',
  })
  assert.deepEqual(productionAccessState(readyBinding, {
    published: true,
    publication: { ready: true, url: ' https://app.example.test ' },
  }), {
    label: 'Live',
    tone: 'success',
    url: 'https://app.example.test',
  })
})

test('keeps production copy deployment-focused while directing unpublished apps to access controls', () => {
  assert.equal(
    productionDeploymentDescription({ phase: 'Ready' }, null),
    'The production deployment is running. Checking external access…',
  )
  assert.equal(
    productionDeploymentDescription({ phase: 'Ready' }, { published: false }),
    'Production is running with no active access policy. Use Share to choose who can open it.',
  )
  assert.equal(
    productionDeploymentDescription({ phase: 'Ready' }, {
      published: true,
      publication: { ready: false },
    }),
    'The production deployment is running while external access is being enabled.',
  )
  assert.equal(
    productionDeploymentDescription({ phase: 'Ready' }, {
      published: true,
      publication: { ready: true, url: 'https://app.example.test' },
    }),
    'The production deployment is running and externally accessible.',
  )
})
