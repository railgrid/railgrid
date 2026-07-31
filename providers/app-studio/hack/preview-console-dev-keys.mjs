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

import {
  createHash,
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
  randomUUID,
} from "node:crypto";
import {
  chmod,
  mkdir,
  open,
  readFile,
  rename,
  rm,
  stat,
} from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

const privateKeyFile = "private-key.pem";
const verificationJWKSFile = "verification-jwks.json";
const keyIDFile = "key-id";
const lockFile = ".generate.lock";
const staleLockMilliseconds = 30_000;

const pause = (milliseconds) =>
  new Promise((resolve) => setTimeout(resolve, milliseconds));

async function acquireLock(outputDirectory) {
  const lockPath = path.join(outputDirectory, lockFile);
  const token = randomUUID();
  for (let attempt = 0; attempt < 200; attempt++) {
    try {
      const handle = await open(lockPath, "wx", 0o600);
      await handle.writeFile(`${JSON.stringify({ pid: process.pid, token })}\n`);
      await handle.close();
      return async () => {
        try {
          const owner = JSON.parse(await readFile(lockPath, "utf8"));
          if (owner.token === token) await rm(lockPath, { force: true });
        } catch (error) {
          if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) {
            throw error;
          }
        }
      };
    } catch (error) {
      if (error?.code !== "EEXIST") throw error;
      try {
        const owner = JSON.parse(await readFile(lockPath, "utf8"));
        const info = await stat(lockPath);
        const ownerPIDValid = Number.isSafeInteger(owner.pid) && owner.pid > 0;
        let ownerAlive = false;
        if (ownerPIDValid) {
          try {
            process.kill(owner.pid, 0);
            ownerAlive = true;
          } catch (signalError) {
            ownerAlive = signalError?.code === "EPERM";
          }
        }
        if (
          (ownerPIDValid && !ownerAlive) ||
          (!ownerPIDValid &&
            Date.now() - info.mtimeMs > staleLockMilliseconds)
        ) {
          await rm(lockPath, { force: true });
          continue;
        }
      } catch (lockError) {
        if (lockError?.code !== "ENOENT") {
          try {
            const info = await stat(lockPath);
            if (Date.now() - info.mtimeMs > staleLockMilliseconds) {
              await rm(lockPath, { force: true });
              continue;
            }
          } catch (statError) {
            if (statError?.code !== "ENOENT") throw statError;
          }
        }
      }
      await pause(50);
    }
  }
  throw new Error(`timed out waiting for ${lockPath}`);
}

async function writeAtomicallyIfChanged(filePath, content, mode) {
  try {
    if ((await readFile(filePath, "utf8")) === content) {
      await chmod(filePath, mode);
      return;
    }
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }

  const temporaryPath = `${filePath}.${process.pid}.${Date.now()}.tmp`;
  const handle = await open(temporaryPath, "wx", mode);
  try {
    await handle.writeFile(content);
    await handle.sync();
  } finally {
    await handle.close();
  }
  await rename(temporaryPath, filePath);
  await chmod(filePath, mode);
}

function publicConfiguration(privateKeyPEM) {
  const privateKey = createPrivateKey(privateKeyPEM);
  const publicKey = createPublicKey(privateKey);
  const publicJWK = publicKey.export({ format: "jwk" });
  if (
    privateKey.asymmetricKeyType !== "ec" ||
    publicJWK.kty !== "EC" ||
    publicJWK.crv !== "P-256"
  ) {
    throw new Error("preview-console signing key must be an EC P-256 key");
  }
  const publicDER = publicKey.export({ format: "der", type: "spki" });
  const keyID = `local-${createHash("sha256")
    .update(publicDER)
    .digest("hex")
    .slice(0, 16)}`;
  return {
    keyID,
    jwks: {
      keys: [{ ...publicJWK, kid: keyID, alg: "ES256", use: "sig" }],
    },
  };
}

export async function ensurePreviewConsoleDevKeys(outputDirectory) {
  if (!outputDirectory) throw new Error("output directory is required");
  const resolvedDirectory = path.resolve(outputDirectory);
  await mkdir(resolvedDirectory, { recursive: true, mode: 0o700 });
  await chmod(resolvedDirectory, 0o700);

  const release = await acquireLock(resolvedDirectory);
  try {
    const privateKeyPath = path.join(resolvedDirectory, privateKeyFile);
    let privateKeyPEM;
    try {
      privateKeyPEM = await readFile(privateKeyPath, "utf8");
      publicConfiguration(privateKeyPEM);
      await chmod(privateKeyPath, 0o600);
    } catch {
      ({ privateKey: privateKeyPEM } = generateKeyPairSync("ec", {
        namedCurve: "P-256",
        privateKeyEncoding: { format: "pem", type: "pkcs8" },
        publicKeyEncoding: { format: "pem", type: "spki" },
      }));
      await writeAtomicallyIfChanged(privateKeyPath, privateKeyPEM, 0o600);
    }

    const { keyID, jwks } = publicConfiguration(privateKeyPEM);
    const verificationJWKSPath = path.join(
      resolvedDirectory,
      verificationJWKSFile,
    );
    const keyIDPath = path.join(resolvedDirectory, keyIDFile);
    await writeAtomicallyIfChanged(
      verificationJWKSPath,
      `${JSON.stringify(jwks)}\n`,
      0o644,
    );
    await writeAtomicallyIfChanged(keyIDPath, `${keyID}\n`, 0o644);
    return { privateKeyPath, verificationJWKSPath, keyIDPath, keyID };
  } finally {
    await release();
  }
}

function outputDirectoryArgument(argv) {
  const index = argv.indexOf("--output-dir");
  if (index < 0 || !argv[index + 1]) {
    throw new Error("usage: preview-console-dev-keys.mjs --output-dir <path>");
  }
  return argv[index + 1];
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href
) {
  ensurePreviewConsoleDevKeys(outputDirectoryArgument(process.argv.slice(2)))
    .then(({ keyID, privateKeyPath, verificationJWKSPath }) => {
      process.stdout.write(
        `Preview-console local key ready (${keyID})\n` +
          `  private: ${privateKeyPath}\n` +
          `  public:  ${verificationJWKSPath}\n`,
      );
    })
    .catch((error) => {
      process.stderr.write(`${error.stack || error.message || error}\n`);
      process.exitCode = 1;
    });
}
