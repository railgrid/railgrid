import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageDir = dirname(fileURLToPath(import.meta.url));
const scratch = await mkdtemp(join(tmpdir(), 'railgrid-actions-install-'));

function singlePackedArtifact(output) {
  let result;
  try {
    result = JSON.parse(output);
  } catch (error) {
    throw new Error(`npm pack --json returned invalid JSON: ${error.message}`, { cause: error });
  }

  assert.ok(result && typeof result === 'object', 'npm pack --json must return an object or array');
  const packed = Array.isArray(result)
    ? result
    : (typeof result.filename === 'string' ? [result] : Object.values(result));
  assert.equal(packed.length, 1, 'npm pack --json must describe exactly one package');

  const artifact = packed[0];
  assert.ok(artifact && typeof artifact === 'object' && !Array.isArray(artifact),
    'npm pack --json must describe the packed artifact with an object');
  assert.ok(typeof artifact.filename === 'string' && artifact.filename.length > 0,
    'npm pack --json artifact must include a filename');
  assert.equal(basename(artifact.filename), artifact.filename,
    'npm pack --json artifact filename must not include a path');
  return artifact;
}

try {
  const artifact = singlePackedArtifact(execFileSync('npm', [
    'pack', '--json', '--pack-destination', scratch,
  ], { cwd: packageDir, encoding: 'utf8' }));
  const packageMetadata = JSON.parse(await readFile(join(packageDir, 'package.json'), 'utf8'));
  assert.equal(artifact.name, packageMetadata.name, 'npm pack returned a different package');
  assert.equal(artifact.version, packageMetadata.version, 'npm pack returned a different version');

  const consumer = join(scratch, 'consumer');
  await mkdir(consumer);
  await writeFile(join(consumer, 'package.json'), JSON.stringify({
    name: 'actions-node-install-smoke',
    private: true,
    type: 'module',
    dependencies: {
      '@railgrid/actions-node': `file:${join(scratch, artifact.filename)}`,
    },
  }, null, 2));
  await writeFile(join(consumer, 'verify.mjs'), [
    "import assert from 'node:assert/strict';",
    "import { createActionsClient } from '@railgrid/actions-node';",
    "assert.equal(typeof createActionsClient, 'function');",
    '',
  ].join('\n'));

  execFileSync('npm', [
    'install', '--ignore-scripts', '--no-audit', '--no-fund', '--package-lock=false',
  ], { cwd: consumer, stdio: 'inherit' });
  execFileSync(process.execPath, ['verify.mjs'], { cwd: consumer, stdio: 'inherit' });
} finally {
  await rm(scratch, { recursive: true, force: true });
}
