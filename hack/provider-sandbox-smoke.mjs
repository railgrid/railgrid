#!/usr/bin/env node
import { spawn } from 'node:child_process'
import { setTimeout as delay } from 'node:timers/promises'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const portal = resolve(root, 'portal')
const children = []
let stopping = false

function start(command, args, cwd) {
  const child = spawn(command, args, { cwd, stdio: 'inherit' })
  children.push(child)
  child.on('exit', code => { if (!stopping && code !== 0) stop(code || 1) })
  return child
}

function stop(code = 0) {
  if (stopping) return
  stopping = true
  for (const child of children) child.kill('SIGTERM')
  process.exitCode = code
}

process.on('SIGINT', () => stop())
process.on('SIGTERM', () => stop())
start(process.execPath, [resolve(portal, 'node_modules/vite/bin/vite.js'), '--config', resolve(root, 'hack/provider-sandbox-smoke/vite.config.mjs')], portal)
start('go', ['run', './hack/provider-sandbox-smoke'], root)

async function waitFor(url, label) {
  const deadline = Date.now() + 60_000
  while (Date.now() < deadline && !stopping) {
    try { if ((await fetch(url)).ok) return } catch {}
    await delay(250)
  }
  throw new Error(`${label} did not become ready at ${url}`)
}

try {
  await Promise.all([
    waitFor('http://127.0.0.1:5174/ui/src/providers/providerSandbox.smoke.ts', 'Vite'),
    waitFor('http://127.0.0.1:4173/', 'sandbox fixture'),
  ])
  console.log('\nOpen http://127.0.0.1:4173/; the page should report PASS. Press Ctrl-C to stop both servers.\n')
} catch (error) {
  console.error(error)
  stop(1)
}
