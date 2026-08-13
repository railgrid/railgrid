import { readdir } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const portalDir = fileURLToPath(new URL('.', import.meta.url))
const sourceDir = new URL('./src/', import.meta.url)
const tests = (await readdir(sourceDir))
  .filter((name) => name.endsWith('.test.mjs'))
  .sort((left, right) => left.localeCompare(right, 'en'))

const failures = []
for (const name of tests) {
  const exitCode = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ['--test', `src/${name}`], {
      cwd: portalDir,
      stdio: 'inherit',
    })
    child.on('error', reject)
    child.on('exit', (code, signal) => {
      if (signal) reject(new Error(`${name} terminated by ${signal}`))
      else resolve(code ?? 1)
    })
  })
  if (exitCode !== 0) failures.push(name)
}

if (failures.length > 0) {
  throw new Error(`${failures.length} portal test file(s) failed: ${failures.join(', ')}`)
}
