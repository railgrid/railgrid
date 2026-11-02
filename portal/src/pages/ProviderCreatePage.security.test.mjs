import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const source = await readFile(new URL('./ProviderCreatePage.vue', import.meta.url), 'utf8')

test('the registration credential is discarded when identity or admin authority changes', () => {
  assert.match(source, /useAuthStore\(\)/)
  assert.match(source, /auth\.user\?\.userId \|\| auth\.user\?\.email/)
  assert.match(source, /watch\(canManage, allowed => \{ if \(!allowed\) reset\(\) \}\)/)
  assert.match(source, /watch\(\(\) => `\$\{auth\.user\?\.userId \|\| auth\.user\?\.email \|\| ''\}\/\$\{canManage\.value\}`\, reset\)/)
  assert.match(source, /function reset\(\) \{\s+generation\+\+/)
  assert.match(source, /generation === current && tenant\.orgUUID === org && canManage\.value/)
})

test('registration form fails closed until the hub reports hosted enrollment available', () => {
  assert.match(source, /const enrollmentAvailable = computed\(\(\) => orgProviders\.enrollment\?\.available === true\)/)
  assert.match(source, /if \(busy\.value \|\| !canManage\.value \|\| !enrollmentAvailable\.value/)
  assert.match(source, /v-if="!enrollmentAvailable"[^>]*role="status"/)
  assert.match(source, /:disabled="busy \|\| !enrollmentAvailable \|\| !target/)
  assert.match(source, /:disabled="busy \|\| !enrollmentAvailable"/)
  assert.match(source, /When hosted enrollment is available, registration creates a provider workspace/)
})
