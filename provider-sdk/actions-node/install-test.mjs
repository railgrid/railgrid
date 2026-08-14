import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageDir = dirname(fileURLToPath(import.meta.url));
const scratch = await mkdtemp(join(tmpdir(), 'faros-actions-install-'));

try {
  const packed = JSON.parse(execFileSync('npm', [
    'pack', '--json', '--pack-destination', scratch,
  ], { cwd: packageDir, encoding: 'utf8' }));
  assert.equal(packed.length, 1);

  const consumer = join(scratch, 'consumer');
  await mkdir(consumer);
  await writeFile(join(consumer, 'package.json'), JSON.stringify({
    name: 'actions-node-install-smoke',
    private: true,
    type: 'module',
    dependencies: {
      '@faros/actions-node': `file:${join(scratch, packed[0].filename)}`,
    },
  }, null, 2));
  await writeFile(join(consumer, 'verify.mjs'), [
    "import assert from 'node:assert/strict';",
    "import { createActionsClient } from '@faros/actions-node';",
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
