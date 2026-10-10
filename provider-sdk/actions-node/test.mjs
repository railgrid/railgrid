import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import {
  ActionsClientError,
  ProviderActionError,
  createActionsClient,
} from './index.mjs';

const resourceRef = {
  name: 'orders',
  apiVersion: 'example.railgrid.ai/v1alpha1',
  kind: 'Table',
  resource: 'tables',
};
const actionsBaseURL = 'https://hub.example/clusters/cluster-a/apis/ai.railgrid.ai/v1alpha1/projects/demo/integration-actions';

function success(result = { rows: [{ id: 1 }] }, overrides = {}) {
  return {
    requestID: 'request-1',
    provider: 'databricks',
    action: 'query_table',
    actionVersion: 'v1',
    resourceRef,
    result,
    ...overrides,
  };
}

test('invokes the project custom subresource and unwraps the stable result', async () => {
  let request;
  const client = createActionsClient({
    baseURL: `${actionsBaseURL}/`,
    token: 'caller-token',
    fetch: async (url, options) => {
      request = { url, options };
      return new Response(JSON.stringify(success()), { status: 200 });
    },
  });

  const result = await client.integration('sales').invoke('query_table/v1', { limit: 1 });

  assert.deepEqual(result, { rows: [{ id: 1 }] });
  assert.equal(request.url, `${actionsBaseURL}/sales`);
  assert.equal(request.options.headers.Authorization, 'Bearer caller-token');
  assert.deepEqual(JSON.parse(request.options.body), {
    action: 'query_table',
    actionVersion: 'v1',
    input: { limit: 1 },
  });
  assert.equal(request.options.redirect, 'error');
});

test('uses the App Studio-injected cluster-qualified action URL', async () => {
  const prior = {
    base: process.env.RAILGRID_ACTIONS_BASE_URL,
  };
  try {
    process.env.RAILGRID_ACTIONS_BASE_URL = actionsBaseURL;
    let request;
    const client = createActionsClient({
      token: 'token',
      fetch: async (url, options) => {
        request = { url, options };
        return new Response(JSON.stringify(success()), { status: 200 });
      },
    });
    await client.integration('sales').invoke('lookup/v1');
    assert.equal(request.url, `${actionsBaseURL}/sales`);
    assert.equal(request.options.headers['X-Railgrid-Org'], undefined);
    assert.equal(request.options.headers['X-Railgrid-Workspace'], undefined);
  } finally {
    for (const [name, value] of Object.entries({
      RAILGRID_ACTIONS_BASE_URL: prior.base,
    })) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  }
});

test('rejects non-loopback HTTP unless explicitly enabled for local tests', () => {
  assert.throws(
    () => createActionsClient({ baseURL: 'http://hub.example/clusters/cluster-a/apis/ai.railgrid.ai/v1alpha1/projects/demo/integration-actions', token: 'token' }),
    (error) => error instanceof ActionsClientError && error.code === 'invalid_config',
  );
  assert.doesNotThrow(() => createActionsClient({
    baseURL: 'http://127.0.0.1:8080/clusters/cluster-a/apis/ai.railgrid.ai/v1alpha1/projects/demo/integration-actions',
    token: 'token', allowInsecureLoopback: true,
  }));
});

test('requires the exact cluster-qualified App Studio Project route', () => {
  const valid = 'https://hub.example/clusters/root/apis/ai.railgrid.ai/v1alpha1/projects/team.apps/integration-actions';
  assert.doesNotThrow(() => createActionsClient({ baseURL: valid, token: 'token' }));
  for (const baseURL of [
    'https://hub.example/services/providers/app-studio',
    'https://hub.example/clusters/cluster-a/apis/ai.railgrid.ai/v1alpha1/projects/demo/integration-actions/sales',
    'https://hub.example/clusters//cluster-a/apis/ai.railgrid.ai/v1alpha1/projects/demo/integration-actions',
    'https://hub.example/clusters/root:tenant/apis/ai.railgrid.ai/v1alpha1/projects/demo/integration-actions',
    'https://hub.example/clusters/cluster-a/apis/other.ai/v1alpha1/projects/demo/integration-actions',
  ]) {
    assert.throws(
      () => createActionsClient({ baseURL, token: 'token' }),
      (error) => error instanceof ActionsClientError && error.failureKind === 'configuration',
      `baseURL unexpectedly accepted: ${baseURL}`,
    );
  }
});

test('returns the complete stable envelope and supports generic actions', async () => {
  let body;
  const client = createActionsClient({
    baseURL: actionsBaseURL,
    token: 'token',
    fetch: async (_url, options) => {
      body = JSON.parse(options.body);
      return new Response(JSON.stringify(success({ ok: true }, {
        provider: 'other-provider',
        action: 'lookup',
        actionVersion: 'v2',
      })), { status: 200 });
    },
  });

  const envelope = await client.integration('sales').invokeEnvelope('lookup/v2', {
    sql: 'provider-defined input',
    options: { limit: 5 },
  });

  assert.equal(envelope.provider, 'other-provider');
  assert.deepEqual(envelope.result, { ok: true });
  assert.equal(body.action, 'lookup');
  assert.equal(body.actionVersion, 'v2');
  assert.deepEqual(body.input, { sql: 'provider-defined input', options: { limit: 5 } });
});

test('generic invocation uses the versioned action contract', async () => {
  let body;
  const client = createActionsClient({
    baseURL: actionsBaseURL,
    token: 'token',
    fetch: async (_url, options) => {
      body = JSON.parse(options.body);
      return new Response(JSON.stringify(success([])), { status: 200 });
    },
  });

  await client.integration('sales').invoke('query_table/v1', { columns: ['id'] });
  assert.equal(body.action, 'query_table');
  assert.equal(body.actionVersion, 'v1');
});

test('propagates idempotency, correlation, deadline, signal, and custom headers', async () => {
  let request;
  const controller = new AbortController();
  const client = createActionsClient({
    baseURL: actionsBaseURL,
    token: 'token',
    fetch: async (url, options) => {
      request = { url, options };
      return new Response(JSON.stringify(success()), { status: 200 });
    },
  });

  await client.integration('sales').invoke('lookup/v1', { key: 'order-1' }, {
    signal: controller.signal,
    idempotencyKey: 'idem-1',
    correlationId: 'corr-1',
    actionDeadlineMs: 45000,
    headers: { 'X-Test': 'present' },
  });

  assert.equal(request.options.headers['Idempotency-Key'], 'idem-1');
  assert.equal(request.options.headers['X-Request-ID'], 'corr-1');
  assert.equal(request.options.headers['X-Railgrid-Action-Deadline-Ms'], '45000');
  assert.equal(request.options.headers['X-Test'], 'present');
  assert.equal(request.options.signal.aborted, false);
});

test('refreshes a dynamic credential once after HTTP 401', async () => {
  const calls = [];
  const requests = [];
  const client = createActionsClient({
    baseURL: actionsBaseURL,
    getToken: async ({ forceRefresh }) => {
      calls.push(forceRefresh);
      return forceRefresh ? 'fresh-token' : 'stale-token';
    },
    fetch: async (_url, options) => {
      requests.push(options.headers.Authorization);
      if (requests.length === 1) return new Response('{}', { status: 401 });
      return new Response(JSON.stringify(success()), { status: 200 });
    },
  });

  await client.integration('sales').invoke('lookup/v1');
  assert.deepEqual(calls, [false, true]);
  assert.deepEqual(requests, ['Bearer stale-token', 'Bearer fresh-token']);
});

test('reads the atomically refreshed token file on every request', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'railgrid-actions-sdk-'));
  const tokenPath = join(dir, 'token');
  const requests = [];
  try {
    await writeFile(tokenPath, 'first-token\n', { mode: 0o600 });
    const client = createActionsClient({
      baseURL: actionsBaseURL,
      tokenFile: tokenPath,
      fetch: async (_url, options) => {
        requests.push(options.headers.Authorization);
        return new Response(JSON.stringify(success()), { status: 200 });
      },
    });

    await client.integration('sales').invoke('query_table/v1');
    await writeFile(tokenPath, 'second-token\n', { mode: 0o600 });
    await client.integration('sales').invoke('query_table/v1');
    assert.deepEqual(requests, ['Bearer first-token', 'Bearer second-token']);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test('uses RAILGRID_ACTIONS_TOKEN_FILE when no credential option is supplied', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'railgrid-actions-sdk-'));
  const tokenPath = join(dir, 'token');
  const prior = process.env.RAILGRID_ACTIONS_TOKEN_FILE;
  try {
    await writeFile(tokenPath, 'env-token\n', { mode: 0o600 });
    process.env.RAILGRID_ACTIONS_TOKEN_FILE = tokenPath;
    let authorization;
    const client = createActionsClient({
      baseURL: actionsBaseURL,
      fetch: async (_url, options) => {
        authorization = options.headers.Authorization;
        return new Response(JSON.stringify(success()), { status: 200 });
      },
    });
    await client.integration('sales').invoke('query_table/v1');
    assert.equal(authorization, 'Bearer env-token');
  } finally {
    if (prior === undefined) delete process.env.RAILGRID_ACTIONS_TOKEN_FILE;
    else process.env.RAILGRID_ACTIONS_TOKEN_FILE = prior;
    await rm(dir, { recursive: true, force: true });
  }
});

test('reports an unavailable token file as a typed credential error', async () => {
  const client = createActionsClient({
    baseURL: actionsBaseURL,
    tokenFile: '/path/that/does/not/exist',
    fetch: async () => new Response(JSON.stringify(success()), { status: 200 }),
  });
  await assert.rejects(
    () => client.integration('sales').invoke('query_table/v1'),
    (error) => error instanceof ActionsClientError
      && error.code === 'credential_file_unavailable'
      && error.failureKind === 'credentials',
  );
});

test('exposes stable provider errors as typed errors', async () => {
  const client = createActionsClient({
    baseURL: actionsBaseURL,
    token: 'token',
    fetch: async () => new Response(JSON.stringify({
      requestID: 'request-2',
      provider: 'databricks',
      action: 'query_table',
      actionVersion: 'v1',
      resourceRef,
      error: { code: 'provider_denied', message: 'table access denied', retryable: false },
    }), { status: 403 }),
  });

  await assert.rejects(
    () => client.integration('sales').invoke('query_table/v1'),
    (error) => error instanceof ProviderActionError
      && error.code === 'provider_denied'
      && error.status === 403
      && error.requestID === 'request-2'
      && error.provider === 'databricks'
      && error.retryable === false
      && error.failureKind === 'authorization'
      && error.message === 'table access denied',
  );
});

test('rejects malformed success envelopes and non-envelope HTTP errors', async () => {
  const malformed = createActionsClient({
    baseURL: actionsBaseURL, token: 'token',
    fetch: async () => new Response(JSON.stringify({ result: { rows: [] } }), { status: 200 }),
  });
  await assert.rejects(
    () => malformed.integration('sales').invoke('query_table/v1'),
    (error) => error instanceof ActionsClientError && error.code === 'invalid_response',
  );

  const failure = createActionsClient({
    baseURL: actionsBaseURL, token: 'token',
    fetch: async () => new Response(JSON.stringify({ message: 'revoked' }), { status: 403 }),
  });
  await assert.rejects(
    () => failure.integration('sales').invoke('query_table/v1'),
    (error) => error instanceof ActionsClientError
      && error.code === 'authorization_failed'
      && error.failureKind === 'authorization'
      && error.status === 403
      && error.message === 'revoked',
  );
});

test('classifies route, authentication, authorization, and upstream failures separately', async () => {
  const cases = [
    { status: 404, code: 'route_not_found', kind: 'route' },
    { status: 401, code: 'authentication_failed', kind: 'authentication' },
    { status: 403, code: 'authorization_failed', kind: 'authorization' },
    { status: 502, code: 'upstream_failed', kind: 'upstream' },
  ];
  for (const expected of cases) {
    const client = createActionsClient({
      baseURL: actionsBaseURL,
      token: 'token',
      fetch: async () => new Response(JSON.stringify({ message: 'diagnostic' }), { status: expected.status }),
    });
    await assert.rejects(
      () => client.integration('sales').invoke('query_table/v1'),
      (error) => error instanceof ActionsClientError
        && error.code === expected.code
        && error.failureKind === expected.kind
        && error.status === expected.status,
    );
  }
});

test('does not infer development credentials', async () => {
  const prior = process.env.RAILGRID_ACTIONS_DEV_TOKEN;
  process.env.RAILGRID_ACTIONS_DEV_TOKEN = 'local-token';
  try {
    const client = createActionsClient({
      baseURL: actionsBaseURL, devToken: 'synthetic-token', allowDevelopmentToken: true,
      fetch: async () => new Response('{}'),
    });
    await assert.rejects(
      () => client.integration('sales').invoke('query_table/v1'),
      (error) => error instanceof ActionsClientError && error.code === 'credential_required',
    );
  } finally {
    if (prior === undefined) delete process.env.RAILGRID_ACTIONS_DEV_TOKEN;
    else process.env.RAILGRID_ACTIONS_DEV_TOKEN = prior;
  }
});

test('supports caller abort and local timeout with typed errors', async () => {
  const abortController = new AbortController();
  const abortClient = createActionsClient({
    baseURL: actionsBaseURL, token: 'token',
    fetch: async (_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
      abortController.abort();
    }),
  });
  await assert.rejects(
    () => abortClient.integration('sales').invoke('query_table/v1', {}, { signal: abortController.signal }),
    (error) => error instanceof ActionsClientError && error.code === 'aborted',
  );

  const timeoutClient = createActionsClient({
    baseURL: actionsBaseURL, token: 'token',
    fetch: async (_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('timed out');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }),
  });
  await assert.rejects(
    () => timeoutClient.integration('sales').invoke('query_table/v1', {}, { timeoutMs: 1 }),
    (error) => error instanceof ActionsClientError && error.code === 'timeout',
  );
});

test('fails closed when browser globals are present', () => {
  const priorWindow = globalThis.window;
  globalThis.window = {};
  try {
    assert.throws(
      () => createActionsClient({ baseURL: actionsBaseURL, token: 'token' }),
      /server-only/,
    );
  } finally {
    if (priorWindow === undefined) delete globalThis.window;
    else globalThis.window = priorWindow;
  }
});
