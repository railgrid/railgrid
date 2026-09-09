import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-assistant-verification', configFile: false, server: { middlewareMode: true } })
const { assistantVerificationBanner } = await vite.ssrLoadModule('/src/assistantVerification.ts')
test.after(async () => vite.close())

test('a failed receipt renders an error banner with the provider summary and blockers', () => {
  const banner = assistantVerificationBanner({
    assistantVerification: {
      outcome: 'failed',
      summary: '  The development sandbox is not running the latest workspace code: the last sync failed.  ',
      blockers: [
        'the project workspace was rebuilt from git commit e1cf71c618b4 (1 files) after a replica change; workspace revision 7 and every uncommitted edit before it are no longer on disk.',
        '',
        ' the last workspace sync after replace_file failed: no package.json ',
      ],
    },
  })
  assert.equal(banner?.tone, 'error')
  assert.equal(banner?.title, 'Development runtime is not running this work')
  assert.equal(banner?.summary, 'The development sandbox is not running the latest workspace code: the last sync failed.')
  assert.equal(banner?.blockers.length, 2)
  assert.match(banner?.blockers[0] ?? '', /rebuilt from git commit e1cf71c618b4/)
  assert.equal(banner?.blockers[1], 'the last workspace sync after replace_file failed: no package.json')
})

test('a stale receipt is a warning and gets a default summary', () => {
  const banner = assistantVerificationBanner({ assistantVerification: { outcome: 'stale' } })
  assert.equal(banner?.tone, 'warning')
  assert.equal(banner?.title, 'Development runtime is behind this work')
  assert.match(banner?.summary ?? '', /has not picked up/)
  assert.deepEqual(banner?.blockers, [])
})

test('positive, absent and malformed receipts render nothing', () => {
  for (const metadata of [
    undefined,
    null,
    {},
    { assistantVerification: 'failed' },
    { assistantVerification: { outcome: 'runtime_verified', summary: 'stale text', blockers: ['old'] } },
    { assistantVerification: { outcome: 'not_verified' } },
    { assistantVerification: { outcome: 'interactions_verified' } },
  ]) {
    assert.equal(assistantVerificationBanner(metadata), null, JSON.stringify(metadata))
  }
})

test('detail is bounded so a runaway reason cannot flood the message', () => {
  const banner = assistantVerificationBanner({
    assistantVerification: {
      outcome: 'failed',
      summary: 's'.repeat(400),
      blockers: Array.from({ length: 9 }, (_, index) => `${index}-${'b'.repeat(400)}`),
    },
  })
  assert.ok(banner)
  assert.equal(banner.summary.length, 240)
  assert.ok(banner.summary.endsWith('…'))
  assert.equal(banner.blockers.length, 4)
  for (const blocker of banner.blockers) {
    assert.equal(blocker.length, 320)
    assert.ok(blocker.endsWith('…'))
  }
})
