/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import assert from "node:assert/strict";
import { createPrivateKey, createPublicKey } from "node:crypto";
import {
  mkdtemp,
  readFile,
  rm,
  stat,
  unlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { ensurePreviewConsoleDevKeys } from "./preview-console-dev-keys.mjs";

test("generates a stable matching P-256 key and public JWKS", async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "preview-console-keys-"));
  t.after(() => rm(directory, { recursive: true, force: true }));

  const first = await ensurePreviewConsoleDevKeys(directory);
  const privateKeyPEM = await readFile(first.privateKeyPath, "utf8");
  const privateKey = createPrivateKey(privateKeyPEM);
  const expectedPublicJWK = createPublicKey(privateKey).export({ format: "jwk" });
  const jwks = JSON.parse(await readFile(first.verificationJWKSPath, "utf8"));

  assert.equal(privateKey.asymmetricKeyType, "ec");
  assert.equal(expectedPublicJWK.crv, "P-256");
  assert.equal(jwks.keys.length, 1);
  assert.equal(jwks.keys[0].x, expectedPublicJWK.x);
  assert.equal(jwks.keys[0].y, expectedPublicJWK.y);
  assert.equal(jwks.keys[0].kid, first.keyID);
  assert.equal(jwks.keys[0].alg, "ES256");
  assert.equal(jwks.keys[0].use, "sig");
  assert.equal("d" in jwks.keys[0], false);
  assert.equal((await stat(first.privateKeyPath)).mode & 0o777, 0o600);

  const second = await ensurePreviewConsoleDevKeys(directory);
  assert.equal(second.keyID, first.keyID);
  assert.equal(await readFile(second.privateKeyPath, "utf8"), privateKeyPEM);
});

test("repairs missing public files without rotating the private key", async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "preview-console-keys-"));
  t.after(() => rm(directory, { recursive: true, force: true }));

  const first = await ensurePreviewConsoleDevKeys(directory);
  const privateKeyPEM = await readFile(first.privateKeyPath, "utf8");
  await Promise.all([
    unlink(first.verificationJWKSPath),
    unlink(first.keyIDPath),
  ]);

  const second = await ensurePreviewConsoleDevKeys(directory);
  assert.equal(second.keyID, first.keyID);
  assert.equal(await readFile(second.privateKeyPath, "utf8"), privateKeyPEM);
  assert.equal(
    (await readFile(second.keyIDPath, "utf8")).trim(),
    first.keyID,
  );
});

test("serializes concurrent Tilt and make invocations", async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "preview-console-keys-"));
  t.after(() => rm(directory, { recursive: true, force: true }));

  const results = await Promise.all(
    Array.from({ length: 4 }, () => ensurePreviewConsoleDevKeys(directory)),
  );
  assert.equal(new Set(results.map((result) => result.keyID)).size, 1);

  const privateKey = createPrivateKey(
    await readFile(results[0].privateKeyPath, "utf8"),
  );
  const publicJWK = createPublicKey(privateKey).export({ format: "jwk" });
  const jwks = JSON.parse(
    await readFile(results[0].verificationJWKSPath, "utf8"),
  );
  assert.equal(jwks.keys[0].x, publicJWK.x);
  assert.equal(jwks.keys[0].y, publicJWK.y);
});

test("recovers a lock left by a dead generator process", async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "preview-console-keys-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await writeFile(
    path.join(directory, ".generate.lock"),
    `${JSON.stringify({ pid: 2_147_483_647, token: "abandoned" })}\n`,
    { mode: 0o600 },
  );

  const result = await ensurePreviewConsoleDevKeys(directory);
  assert.match(result.keyID, /^local-[a-f0-9]{16}$/);
});

test("replaces an invalid private key and derives a new public tuple", async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "preview-console-keys-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const first = await ensurePreviewConsoleDevKeys(directory);
  await writeFile(first.privateKeyPath, "not a private key\n", { mode: 0o600 });

  const second = await ensurePreviewConsoleDevKeys(directory);
  assert.notEqual(second.keyID, first.keyID);
  const privateKeyPEM = await readFile(second.privateKeyPath, "utf8");
  assert.doesNotThrow(() => createPrivateKey(privateKeyPEM));
  const jwks = JSON.parse(
    await readFile(second.verificationJWKSPath, "utf8"),
  );
  assert.equal(jwks.keys[0].kid, second.keyID);
  assert.equal("d" in jwks.keys[0], false);
});
