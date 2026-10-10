import { createHash } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { spawnSync } from 'node:child_process'

function npmVersion(run) {
  const result = run('npm', ['--version'], { encoding: 'utf8' })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(result.stderr?.trim() || 'npm --version failed')
  return result.stdout.trim()
}

function dependencyFingerprint(packageRoot, runtime, version) {
  return createHash('sha256')
    .update(readFileSync(resolve(packageRoot, 'package.json')))
    .update('\0')
    .update(readFileSync(resolve(packageRoot, 'package-lock.json')))
    .update('\0')
    .update(JSON.stringify({ ...runtime, npm: version }))
    .digest('hex')
}

export function ensureDependencies({
  packageRoot = process.cwd(),
  nodeVersion = process.version,
  platform = process.platform,
  arch = process.arch,
  run = spawnSync,
} = {}) {
  const root = resolve(packageRoot)
  const stampPath = resolve(root, 'node_modules/.railgrid-dependencies.sha256')
  const vitePath = resolve(root, 'node_modules/vite/bin/vite.js')
  const runtime = { node: nodeVersion, platform, arch }
  const version = npmVersion(run)
  const fingerprint = dependencyFingerprint(root, runtime, version)
  const installedFingerprint = existsSync(stampPath) ? readFileSync(stampPath, 'utf8').trim() : ''

  if (installedFingerprint === fingerprint && existsSync(vitePath)) {
    return { installed: false, fingerprint }
  }

  const install = run('npm', ['ci', '--include=dev', '--no-audit', '--no-fund'], { cwd: root, stdio: 'inherit' })
  if (install.error) throw install.error
  if (install.status !== 0) {
    const error = new Error(install.stderr?.trim() || `npm ci failed with status ${install.status ?? 'unknown'}`)
    error.status = install.status ?? 1
    throw error
  }
  if (!existsSync(vitePath)) throw new Error('npm ci completed without installing Vite')
  if (dependencyFingerprint(root, runtime, version) !== fingerprint) {
    throw new Error('npm ci changed package.json or package-lock.json; refusing to cache the install')
  }

  writeFileSync(stampPath, `${fingerprint}\n`)
  return { installed: true, fingerprint }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    ensureDependencies({ packageRoot: fileURLToPath(new URL('..', import.meta.url)) })
  } catch (error) {
    console.error(error)
    process.exitCode = error.status ?? 1
  }
}
