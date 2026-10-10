import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import test from 'node:test'

import { ensureDependencies } from './ensure-dependencies.mjs'

function makeFixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'app-studio-dependencies-'))
  writeFileSync(join(root, 'package.json'), '{"name":"fixture","devDependencies":{"vite":"6.3.0"}}\n')
  writeFileSync(join(root, 'package-lock.json'), '{"name":"fixture","lockfileVersion":3}\n')
  t.after(() => rmSync(root, { recursive: true, force: true }))
  return {
    root,
    stampPath: join(root, 'node_modules/.railgrid-dependencies.sha256'),
    vitePath: join(root, 'node_modules/vite/bin/vite.js'),
  }
}

function fakeNpm({ version = '10.8.2', installStatus = 0, installVite = true, mutateLock = false } = {}) {
  const calls = []
  return {
    calls,
    run(command, args, options) {
      calls.push({ command, args: [...args], options, nodeEnv: process.env.NODE_ENV })
      assert.equal(command, 'npm')
      if (args[0] === '--version') return { status: 0, stdout: `${version}\n`, stderr: '' }
      assert.equal(args[0], 'ci')
      if (installStatus !== 0) return { status: installStatus, stdout: '', stderr: 'fixture install failed' }
      if (mutateLock) {
        writeFileSync(join(options.cwd, 'package-lock.json'), '{"name":"mutated"}\n')
      }
      if (installVite) {
        const vitePath = join(options.cwd, 'node_modules/vite/bin/vite.js')
        mkdirSync(resolve(vitePath, '..'), { recursive: true })
        writeFileSync(vitePath, '// fixture vite entrypoint\n')
      }
      return { status: 0, stdout: '', stderr: '' }
    },
  }
}

function runEnsure(fixture, fake, runtime = {}) {
  return ensureDependencies({ packageRoot: fixture.root, ...runtime, run: fake.run })
}

test('cache hit skips npm ci and leaves the package lock untouched', (t) => {
  const fixture = makeFixture(t)
  const lockBefore = readFileSync(join(fixture.root, 'package-lock.json'))
  const first = fakeNpm()
  assert.equal(runEnsure(fixture, first).installed, true)
  assert.deepEqual(first.calls.map(({ args }) => args[0]), ['--version', 'ci'])
  assert.deepEqual(readFileSync(join(fixture.root, 'package-lock.json')), lockBefore)

  const second = fakeNpm()
  assert.equal(runEnsure(fixture, second).installed, false)
  assert.deepEqual(second.calls.map(({ args }) => args[0]), ['--version'])
})

test('package, lock, Node, npm, platform, and architecture changes invalidate the stamp', async (t) => {
  const scenarios = [
    ['package manifest bytes', (fixture) => writeFileSync(join(fixture.root, 'package.json'), '{"name":"changed"}\n'), {}],
    ['package lock bytes', (fixture) => writeFileSync(join(fixture.root, 'package-lock.json'), '{"name":"changed"}\n'), {}],
    ['Node version', () => undefined, { nodeVersion: 'v99.0.0' }],
    ['npm version', () => undefined, { npmVersion: '11.0.0' }],
    ['platform', () => undefined, { platform: 'fixture-platform' }],
    ['architecture', () => undefined, { arch: 'fixture-arch' }],
  ]

  for (const [name, mutate, changedRuntime] of scenarios) {
    await t.test(name, (subtest) => {
      const fixture = makeFixture(subtest)
      runEnsure(fixture, fakeNpm())
      mutate(fixture)
      const changed = fakeNpm({ version: changedRuntime.npmVersion ?? '10.8.2' })
      assert.equal(runEnsure(fixture, changed, changedRuntime).installed, true)
      assert.deepEqual(changed.calls.map(({ args }) => args[0]), ['--version', 'ci'])
    })
  }
})

test('a matching stamp does not hide a missing Vite entrypoint', (t) => {
  const fixture = makeFixture(t)
  runEnsure(fixture, fakeNpm())
  rmSync(fixture.vitePath)

  const retry = fakeNpm()
  assert.equal(runEnsure(fixture, retry).installed, true)
  assert.deepEqual(retry.calls.map(({ args }) => args[0]), ['--version', 'ci'])
})

test('npm ci explicitly includes dev dependencies in production environments', (t) => {
  const fixture = makeFixture(t)
  const previous = process.env.NODE_ENV
  process.env.NODE_ENV = 'production'
  try {
    const fake = fakeNpm()
    runEnsure(fixture, fake)
    const install = fake.calls.find(({ args }) => args[0] === 'ci')
    assert.ok(install)
    assert.deepEqual(install.args, ['ci', '--include=dev', '--no-audit', '--no-fund'])
    assert.equal(install.nodeEnv, 'production')
  } finally {
    if (previous === undefined) delete process.env.NODE_ENV
    else process.env.NODE_ENV = previous
  }
})

test('failed npm ci does not write an install stamp', (t) => {
  const fixture = makeFixture(t)
  const fake = fakeNpm({ installStatus: 23 })
  assert.throws(() => runEnsure(fixture, fake), (error) => error.status === 23)
  assert.equal(existsSync(fixture.stampPath), false)
})

test('npm ci that mutates the watched lockfile is rejected without a stamp', (t) => {
  const fixture = makeFixture(t)
  const fake = fakeNpm({ mutateLock: true })
  assert.throws(() => runEnsure(fixture, fake), /changed package.json or package-lock.json/)
  assert.equal(existsSync(fixture.stampPath), false)
})
