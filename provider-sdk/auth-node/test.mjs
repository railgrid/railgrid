import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import test from 'node:test';
import { createRailgridAuth, RailgridAuthError } from './index.mjs';

const issuer = 'https://hub.example/auth/apps/identity';
const audience = 'project-uid/production';
const origin = 'https://orders.example';
const environmentNames = ['RAILGRID_AUTH_ISSUER', 'RAILGRID_AUTH_AUDIENCE', 'RAILGRID_AUTH_ORIGIN', 'RAILGRID_AUTH_INSTANCE_UID'];

function setEnvironment(overrides = {}) {
  const previous = Object.fromEntries(environmentNames.map((name) => [name, process.env[name]]));
  for (const name of environmentNames) {
    const value = overrides[name] ?? ({
      RAILGRID_AUTH_ISSUER: issuer,
      RAILGRID_AUTH_AUDIENCE: audience,
      RAILGRID_AUTH_ORIGIN: origin,
      RAILGRID_AUTH_INSTANCE_UID: 'instance-uid-123',
    })[name];
    if (value === undefined) delete process.env[name];
    else process.env[name] = value;
  }
  return () => {
    for (const [name, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  };
}

function assertion(privateKey, overrides = {}, headerOverrides = {}) {
  const nowSeconds = Math.floor(Date.now() / 1000);
  const header = { alg: 'EdDSA', typ: 'JWT', kid: 'hub-key-1', ...headerOverrides };
  const claims = {
    iss: issuer,
    aud: audience,
    sub: 'user-opaque-123',
    iat: nowSeconds,
    exp: nowSeconds + 600,
    railgrid_app_id: audience,
    railgrid_environment: 'production',
    railgrid_instance_uid: 'instance-uid-123',
    name: 'Ada Example',
    email: 'ada@example.test',
    ...overrides,
  };
  const head = Buffer.from(JSON.stringify(header)).toString('base64url');
  const body = Buffer.from(JSON.stringify(claims)).toString('base64url');
  const message = `${head}.${body}`;
  return `${message}.${sign(null, Buffer.from(message), privateKey).toString('base64url')}`;
}

function fixture({ now = Date.now(), failFetch = false } = {}) {
  const { publicKey, privateKey } = generateKeyPairSync('ed25519');
  const exported = publicKey.export({ format: 'jwk' });
  const jwk = { ...exported, alg: 'EdDSA', use: 'sig', kid: 'hub-key-1' };
  let fetchCalls = 0;
  let fetchFails = failFetch;
  const urls = [];
  const auth = createRailgridAuth({
    cacheTTLMs: 5_000,
    now: () => now,
    fetch: async (url, options) => {
      fetchCalls += 1;
      urls.push({ url, options });
      if (fetchFails) throw new Error('network down');
      return new Response(JSON.stringify({ keys: [jwk] }), { status: 200 });
    },
  });
  return {
    auth,
    privateKey,
    identity: assertion(privateKey),
    urls,
    fetchCalls: () => fetchCalls,
    setFetchFails(value) { fetchFails = value; },
    setNow(value) { now = value; },
  };
}

function request(identity, overrides = {}) {
  return {
    method: 'GET',
    headers: { 'x-railgrid-app-identity': identity, ...overrides },
  };
}

async function withEnvironment(run, overrides) {
  const restore = setEnvironment(overrides);
  try {
    await run();
  } finally {
    restore();
  }
}

test('verifies the trusted identity header and exposes only the normal user', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    const user = await f.auth.getUser(request(f.identity));
    assert.deepEqual(user, { id: 'user-opaque-123', displayName: 'Ada Example' });
    assert.equal('email' in user, false);
    assert.equal(f.fetchCalls(), 1);
    assert.equal(f.urls[0].url, `${issuer}/jwks`);
    assert.equal(f.urls[0].options.redirect, 'error');
    assert.equal(f.urls[0].options.credentials, 'omit');
  });
});

test('rejects assertions for a different app, environment, or instance', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    for (const identity of [
      assertion(f.privateKey, { aud: 'another-project/production', railgrid_app_id: 'another-project/production' }),
      assertion(f.privateKey, { railgrid_environment: 'preview' }),
      assertion(f.privateKey, { railgrid_instance_uid: 'another-instance' }),
    ]) {
      await assert.rejects(
        () => f.auth.getUser(request(identity)),
        (error) => error instanceof RailgridAuthError && error.code === 'unauthorized' && error.status === 401,
      );
    }
    assert.equal(f.fetchCalls(), 0, 'wrong-app claims should not reach the JWKS endpoint');
  });
});

test('rejects expired, overlong, and future-issued assertions', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    const nowSeconds = Math.floor(Date.now() / 1000);
    for (const overrides of [
      { exp: nowSeconds - 1 },
      { iat: nowSeconds - 901, exp: nowSeconds + 1 },
      { iat: nowSeconds + 60, exp: nowSeconds + 120 },
    ]) {
      await assert.rejects(
        () => f.auth.getUser(request(assertion(f.privateKey, overrides))),
        (error) => error instanceof RailgridAuthError && error.status === 401,
      );
    }
  });
});

test('does not trust Authorization, req.user, or malformed identity headers', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    const forged = { method: 'GET', headers: { authorization: `Bearer ${f.identity}` }, user: { id: 'forged' } };
    await assert.rejects(() => f.auth.getUser(forged), (error) => error.code === 'unauthorized');
    for (const value of [undefined, ['one', 'two'], 'Bearer token', 'one.two.three']) {
      await assert.rejects(
        () => f.auth.getUser(request(value)),
        (error) => error instanceof RailgridAuthError && error.status === 401,
      );
    }
    assert.equal(f.fetchCalls(), 0);
  });
});

test('optional identity is null only when the Railgrid header is absent', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    assert.equal(await f.auth.getOptionalUser({ method: 'GET', headers: {} }), null);
    assert.equal(await f.auth.getOptionalUser({ method: 'GET', headers: { authorization: 'Bearer forged' } }), null);
    await assert.rejects(
      () => f.auth.getOptionalUser(request('Bearer malformed')),
      (error) => error instanceof RailgridAuthError && error.code === 'unauthorized' && error.status === 401,
    );
    assert.equal(f.fetchCalls(), 0);
  });
});

test('uses one unknown-kid refresh and rejects a forged signature', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    const unknownKid = assertion(f.privateKey, {}, { kid: 'not-published' });
    await assert.rejects(() => f.auth.getUser(request(unknownKid)), (error) => error.status === 401);
    assert.equal(f.fetchCalls(), 1);

    const other = generateKeyPairSync('ed25519');
    const forged = assertion(other.privateKey);
    await assert.rejects(() => f.auth.getUser(request(forged)), (error) => error.status === 401);
    assert.equal(f.fetchCalls(), 1, 'a known kid should use the cache and never cause an extra refresh');
  });
});

test('uses a cached key during a JWKS outage through assertion expiry, then rejects it', async () => {
  await withEnvironment(async () => {
    const start = Date.now();
    const f = fixture({ now: start });
    assert.deepEqual(await f.auth.getUser(request(f.identity)), { id: 'user-opaque-123', displayName: 'Ada Example' });
    const expiry = JSON.parse(Buffer.from(f.identity.split('.')[1], 'base64url')).exp * 1_000;
    f.setNow(start + 6_000);
    f.setFetchFails(true);
    assert.deepEqual(await f.auth.getUser(request(f.identity)), { id: 'user-opaque-123', displayName: 'Ada Example' });
    assert.equal(f.fetchCalls(), 2);

    const other = generateKeyPairSync('ed25519');
    await assert.rejects(() => f.auth.getUser(request(assertion(other.privateKey))), (error) => error.status === 401);
    assert.equal(f.fetchCalls(), 2, 'a stale known kid must not trigger repeated outage refreshes');

    f.setNow(expiry);
    await assert.rejects(() => f.auth.getUser(request(f.identity)), (error) => error.status === 401);
    assert.equal(f.fetchCalls(), 2, 'expired assertions are rejected before consulting cached keys');
  });
});

test('returns 503 when there is no usable cached key during an outage', async () => {
  await withEnvironment(async () => {
    const f = fixture({ failFetch: true });
    await assert.rejects(
      () => f.auth.getUser(request(f.identity)),
      (error) => error instanceof RailgridAuthError && error.code === 'identity_verification_unavailable' && error.status === 503,
    );
  });
});

test('bounds the JWKS response body', async () => {
  await withEnvironment(async () => {
    const { privateKey } = generateKeyPairSync('ed25519');
    const auth = createRailgridAuth({
      fetch: async () => new Response('x'.repeat(70 * 1024), { status: 200 }),
    });
    await assert.rejects(
      () => auth.getUser(request(assertion(privateKey))),
      (error) => error instanceof RailgridAuthError && error.code === 'identity_verification_unavailable' && error.status === 503,
    );
  });
});

test('requires the exact Origin on authenticated mutations', async () => {
  await withEnvironment(async () => {
    const f = fixture();
    await assert.rejects(
      () => f.auth.getUser({ ...request(f.identity), method: 'POST' }),
      (error) => error instanceof RailgridAuthError && error.code === 'origin_mismatch' && error.status === 403,
    );
    await assert.rejects(
      () => f.auth.getUser({ ...request(f.identity, { origin: 'https://evil.example' }), method: 'PATCH' }),
      (error) => error.code === 'origin_mismatch',
    );
    const user = await f.auth.getUser({ ...request(f.identity, { origin }), method: 'POST' });
    assert.equal(user.id, 'user-opaque-123');
  });
});

test('middleware sends structured unauthorized and configuration errors', async () => {
  await withEnvironment(async () => {
    const f = fixture({ failFetch: true });
    const response = {
      headers: {},
      setHeader(name, value) { this.headers[name.toLowerCase()] = value; },
      end(body) { this.body = body; this.onEnd?.(); },
    };
    await new Promise((resolve) => {
      response.onEnd = resolve;
      f.auth.middleware()(request(undefined), response, resolve);
    });
    assert.equal(response.statusCode, 401);
    assert.deepEqual(JSON.parse(response.body), { error: { code: 'unauthorized', message: 'Authentication required' } });
    assert.equal(response.headers['cache-control'], 'no-store');

    const restore = setEnvironment({ RAILGRID_AUTH_ISSUER: '' });
    try {
      let configError;
      assert.throws(
        () => createRailgridAuth(),
        (error) => {
          configError = error;
          return error instanceof RailgridAuthError && error.code === 'configuration_error' && error.status === 500;
        },
      );
      assert.deepEqual(configError.toJSON(), {
        error: { code: 'configuration_error', message: 'RAILGRID_AUTH_ISSUER is required' },
      });
    } finally {
      restore();
    }

    const restoreInstance = setEnvironment({ RAILGRID_AUTH_INSTANCE_UID: '' });
    try {
      assert.throws(
        () => createRailgridAuth(),
        (error) => error instanceof RailgridAuthError && error.code === 'configuration_error'
          && error.message === 'RAILGRID_AUTH_INSTANCE_UID is required',
      );
    } finally {
      restoreInstance();
    }
  });
});

test('rejects insecure and non-canonical trusted configuration', async () => {
  for (const [name, value] of [
    ['RAILGRID_AUTH_ISSUER', 'http://hub.example/auth/apps/identity'],
    ['RAILGRID_AUTH_ISSUER', 'https://hub.example/auth/apps/identity/'],
    ['RAILGRID_AUTH_ISSUER', 'https://hub.example@127.0.0.1/auth/apps/identity'],
    ['RAILGRID_AUTH_ORIGIN', 'http://orders.example'],
    ['RAILGRID_AUTH_ORIGIN', 'https://orders.example/'],
    ['RAILGRID_AUTH_AUDIENCE', 'project-uid'],
    ['RAILGRID_AUTH_INSTANCE_UID', ''],
  ]) {
    await withEnvironment(async () => {
      assert.throws(() => createRailgridAuth(), (error) => error.code === 'configuration_error');
    }, { [name]: value });
  }
});
