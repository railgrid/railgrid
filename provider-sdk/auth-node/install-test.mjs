import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageDir = dirname(fileURLToPath(import.meta.url));
const scratch = await mkdtemp(join(tmpdir(), 'railgrid-auth-node-install-'));
const npmCLI = join(dirname(process.execPath), '..', 'lib', 'node_modules', 'npm', 'bin', 'npm-cli.js');

function runNpm(args, options = {}) {
  return execFileSync(process.execPath, [npmCLI, '--cache', join(scratch, 'npm-cache'), '--logs-max=0', ...args], options);
}

function packedArtifact(output) {
  const result = JSON.parse(output);
  assert.ok(Array.isArray(result) && result.length === 1, 'npm pack must describe exactly one package');
  assert.equal(basename(result[0].filename), result[0].filename, 'packed filename must not include a path');
  return result[0];
}

try {
  const artifact = packedArtifact(runNpm([
    'pack', '--json', '--pack-destination', scratch,
  ], { cwd: packageDir, encoding: 'utf8' }));
  const metadata = JSON.parse(await readFile(join(packageDir, 'package.json'), 'utf8'));
  assert.equal(metadata.name, '@railgrid/auth-node');
  assert.equal(artifact.name, metadata.name);
  assert.equal(artifact.version, metadata.version);

  const consumer = join(scratch, 'consumer');
  await mkdir(consumer);
  await writeFile(join(consumer, 'package.json'), JSON.stringify({
    name: 'railgrid-auth-node-install-smoke',
    private: true,
    type: 'module',
    dependencies: {
      '@railgrid/auth-node': `file:${join(scratch, artifact.filename)}`,
    },
  }, null, 2));
  await writeFile(join(consumer, 'verify.mjs'), [
    "import assert from 'node:assert/strict';",
    "import { createRailgridAuth, RailgridAuthError } from '@railgrid/auth-node';",
    "assert.equal(typeof createRailgridAuth, 'function');",
    "assert.equal(typeof RailgridAuthError, 'function');",
    "globalThis.window = {};",
    "const browserTarget = new URL(import.meta.resolve('@railgrid/auth-node'));",
    "browserTarget.searchParams.set('browser-check', '1');",
    "await assert.rejects(() => import(browserTarget.href), (error) => error.code === 'server_only');",
    "delete globalThis.window;",
    '',
  ].join('\n'));
  runNpm([
    'install', '--offline', '--ignore-scripts', '--no-audit', '--no-fund', '--package-lock=false',
  ], { cwd: consumer, stdio: 'inherit' });
  execFileSync(process.execPath, ['verify.mjs'], { cwd: consumer, stdio: 'inherit' });
} finally {
  await rm(scratch, { recursive: true, force: true });
}
