import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./overlayResize.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext } })
const { resizeOverlay } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
const bounds = { width: 600, height: 500 }

test('resize follows the centered horizontal edge and bottom-anchored vertical edge', () => {
  assert.deepEqual(resizeOverlay({ width: 400, height: 350 }, -10, -20, bounds), { width: 420, height: 370 })
  assert.deepEqual(resizeOverlay({ width: 400, height: 350 }, 10, 20, bounds), { width: 380, height: 330 })
})
test('overshooting either limit does not delay reversal', () => {
  const maximum = resizeOverlay({ width: 400, height: 350 }, -1000, -1000, bounds)
  assert.deepEqual(maximum, bounds)
  assert.deepEqual(resizeOverlay(maximum, 10, 10, bounds), { width: 580, height: 490 })
  const minimum = resizeOverlay(maximum, 1000, 1000, bounds)
  assert.deepEqual(minimum, { width: 320, height: 280 })
  assert.deepEqual(resizeOverlay(minimum, -10, -10, bounds), { width: 340, height: 290 })
})
test('viewport limits take precedence over minimum sizes and previously stored sizes', () => {
  assert.deepEqual(resizeOverlay({ width: 700, height: 600 }, 0, 0, bounds), bounds)
  assert.deepEqual(resizeOverlay({ width: 700, height: 600 }, 10, 10, bounds), { width: 580, height: 490 })
  assert.deepEqual(resizeOverlay({ width: 100, height: 100 }, 0, 0, { width: 304, height: 180 }), { width: 304, height: 180 })
})
