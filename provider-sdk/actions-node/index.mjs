import { readFile } from 'node:fs/promises';

/**
 * Server-side App Studio Provider Actions client.
 *
 * The SDK calls App Studio's project-scoped kcp custom subresource. The base
 * URL is injected by App Studio and contains the tenant cluster and Project;
 * the SDK only appends an integration alias. It never accepts a provider URL
 * or backend topology and must remain server-side because it sends a bearer.
 */

export class ActionsClientError extends Error {
  constructor(message, options = {}) {
    super(String(message ?? 'provider action request failed'));
    this.name = options.name ?? 'ActionsClientError';
    this.code = String(options.code ?? 'provider_action_failed');
    this.status = Number.isInteger(options.status) ? options.status : 0;
    this.requestID = String(options.requestID ?? options.requestId ?? '');
    this.provider = String(options.provider ?? '');
    this.action = String(options.action ?? '');
    this.actionVersion = String(options.actionVersion ?? '');
    this.failureKind = String(options.failureKind ?? failureKindForCode(this.code));
    this.resourceRef = options.resourceRef;
    this.retryable = options.retryable === true;
    this.body = options.body;
    if (options.cause !== undefined) this.cause = options.cause;
  }
}

/** Error returned by a provider action's stable `error` envelope. */
export class ProviderActionError extends ActionsClientError {
  constructor(message, options = {}) {
    super(message, { ...options, name: 'ProviderActionError' });
  }
}

function assertServerOnly() {
  if (typeof window !== 'undefined' || typeof document !== 'undefined') {
    throw new ActionsClientError(
      'The Railgrid Actions SDK is server-only; never expose a caller credential to a browser',
      { code: 'server_only' },
    );
  }
}

function normalizeToken(value) {
  const token = String(value ?? '').trim();
  if (!token) return '';
  return /^Bearer\s+/i.test(token) ? token : `Bearer ${token}`;
}

function joinURL(baseURL, path) {
  const base = String(baseURL ?? '').trim().replace(/\/+$/, '');
  if (!base) throw new ActionsClientError('baseURL is required', { code: 'invalid_config' });
  return `${base}/${String(path).replace(/^\/+/, '')}`;
}

function isLoopbackHost(hostname) {
  const host = String(hostname ?? '').toLowerCase();
  return host === 'localhost' || host === '127.0.0.1' || host === '::1' || host === '[::1]';
}

function isDNS1123Subdomain(name) {
  const value = String(name ?? '');
  if (value.length === 0 || value.length > 253) return false;
  return value.split('.').every((label) =>
    label.length <= 63 && /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/.test(label));
}

function validateBaseURL(raw, allowInsecureLoopback) {
  const value = String(raw ?? '').trim();
  if (!value) throw new ActionsClientError('baseURL is required', { code: 'invalid_config' });
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new ActionsClientError('baseURL must be an absolute HTTPS URL', { code: 'invalid_config' });
  }
  if (!parsed.host || parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new ActionsClientError('baseURL must be an absolute HTTPS URL', { code: 'invalid_config' });
  }
  const basePath = parsed.pathname.replace(/\/$/, '');
  const segments = basePath.split('/').filter(Boolean);
  const expectedPath = segments.length === 8
    ? `/clusters/${segments[1]}/apis/ai.railgrid.ai/v1alpha1/projects/${segments[6]}/integration-actions`
    : '';
  if (
    segments.length !== 8 ||
    segments[0] !== 'clusters' ||
    !/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/.test(segments[1]) ||
    segments[2] !== 'apis' ||
    segments[3] !== 'ai.railgrid.ai' ||
    segments[4] !== 'v1alpha1' ||
    segments[5] !== 'projects' ||
    !isDNS1123Subdomain(segments[6]) ||
    segments[7] !== 'integration-actions' ||
    basePath !== expectedPath
  ) {
    throw new ActionsClientError(
      'baseURL must be the App Studio project integration-actions endpoint under a cluster-qualified kcp path',
      { code: 'invalid_config', failureKind: 'configuration' },
    );
  }
  if (parsed.protocol === 'https:') return `${parsed.origin}${basePath}`;
  if (parsed.protocol === 'http:' && allowInsecureLoopback === true && isLoopbackHost(parsed.hostname)) return `${parsed.origin}${basePath}`;
  throw new ActionsClientError('baseURL must use HTTPS (or explicitly allow HTTP loopback for local tests)', { code: 'invalid_config', failureKind: 'configuration' });
}

function actionPath(alias) {
  return `/${encodeURIComponent(alias)}`;
}

function parseActionID(action) {
  const slash = action.indexOf('/');
  if (slash <= 0 || slash !== action.lastIndexOf('/') || slash === action.length - 1) {
    throw new ActionsClientError('action must use the name/version form', {
      code: 'invalid_request', failureKind: 'configuration', action,
    });
  }
  const name = action.slice(0, slash).trim();
  const version = action.slice(slash + 1).trim();
  if (!/^[A-Za-z0-9_-]{1,63}$/.test(name) || !/^[A-Za-z0-9_-]{1,63}$/.test(version)) {
    throw new ActionsClientError('action name and version must be identifiers', {
      code: 'invalid_request', failureKind: 'configuration', action,
    });
  }
  return { name, version };
}

function isFunction(value) {
  return typeof value === 'function';
}

function providerFunction(options) {
  if (isFunction(options.getToken)) return options.getToken;
  if (isFunction(options.token)) return options.token;
  const provider = options.credentialProvider;
  if (isFunction(provider)) return provider;
  if (provider && isFunction(provider.getToken)) return provider.getToken.bind(provider);
  return undefined;
}

function hasRefreshableCredential(options) {
  return providerFunction(options) !== undefined || tokenFilePath(options) !== '';
}

function tokenFilePath(options) {
  const configured = options.tokenFile ?? process.env.RAILGRID_ACTIONS_TOKEN_FILE;
  return String(configured ?? '').trim();
}

async function resolveCredential(options, { forceRefresh = false, signal } = {}) {
  const provider = providerFunction(options);
  if (provider) {
    let token;
    try {
      token = await provider({ forceRefresh, signal });
    } catch (error) {
      throw new ActionsClientError('credential provider failed', {
        code: 'credential_provider_failed',
        retryable: !forceRefresh,
        cause: error,
      });
    }
    const normalized = normalizeToken(token);
    if (normalized) return normalized;
  } else if (options.token !== undefined) {
    const normalized = normalizeToken(options.token);
    if (normalized) return normalized;
  }
  const file = tokenFilePath(options);
  if (file) {
    let contents;
    try {
      contents = await readFile(file, 'utf8');
    } catch (error) {
      throw new ActionsClientError('the Railgrid caller credential file is unavailable', {
        code: 'credential_file_unavailable',
        retryable: !forceRefresh,
        cause: error,
      });
    }
    const normalized = normalizeToken(contents);
    if (normalized) return normalized;
    throw new ActionsClientError('the Railgrid caller credential file is empty', {
      code: 'credential_file_unavailable',
      retryable: !forceRefresh,
    });
  }
  throw new ActionsClientError(
    'a Railgrid caller credential is required; pass token, tokenFile, getToken, or credentialProvider on the server',
    { code: 'credential_required' },
  );
}

function numberOption(value, fallback) {
  if (value === undefined || value === null || value === '') return fallback;
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) {
    throw new ActionsClientError('timeoutMs and actionDeadlineMs must be non-negative numbers', { code: 'invalid_config' });
  }
  return number;
}

function requestHeaderOptions(clientOptions, requestOptions) {
  const options = { ...clientOptions, ...requestOptions };
  const headers = {
    ...(clientOptions.headers ?? {}),
    ...(requestOptions.headers ?? {}),
  };
  if (options.idempotencyKey !== undefined) headers['Idempotency-Key'] = String(options.idempotencyKey);
  const requestID = options.requestID ?? options.requestId ?? options.correlationID ?? options.correlationId;
  if (requestID !== undefined) headers['X-Request-ID'] = String(requestID);
  const deadline = options.actionDeadlineMs ?? options.deadlineMs;
  if (deadline !== undefined) headers['X-Railgrid-Action-Deadline-Ms'] = String(deadline);
  return { options, headers };
}

function composeSignal(parent, timeoutMs) {
  const timeout = timeoutMs === undefined ? undefined : numberOption(timeoutMs, undefined);
  if (timeout === undefined && !parent) return { signal: undefined, cleanup: () => {}, timedOut: () => false };

  const controller = new AbortController();
  let didTimeout = false;
  let timer;
  const abortFromParent = () => controller.abort(parent?.reason);
  if (parent) {
    if (parent.aborted) abortFromParent();
    else parent.addEventListener('abort', abortFromParent, { once: true });
  }
  if (timeout !== undefined) {
    timer = setTimeout(() => {
      didTimeout = true;
      controller.abort(new Error('provider action request timed out'));
    }, timeout);
  }
  return {
    signal: controller.signal,
    cleanup: () => {
      if (timer !== undefined) clearTimeout(timer);
      if (parent) parent.removeEventListener('abort', abortFromParent);
    },
    timedOut: () => didTimeout,
  };
}

function isAbortError(error) {
  return error?.name === 'AbortError' || error?.code === 'ABORT_ERR';
}

function stableResourceRef(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined;
  const ref = {
    name: String(value.name ?? '').trim(),
    apiVersion: String(value.apiVersion ?? '').trim(),
    kind: String(value.kind ?? '').trim(),
    resource: String(value.resource ?? '').trim(),
  };
  if (!ref.name || !ref.apiVersion || !ref.kind || !ref.resource) return undefined;
  return ref;
}

function decodeJSON(text) {
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

function stableEnvelope(body) {
  if (!body || typeof body !== 'object' || Array.isArray(body)) return undefined;
  const envelope = {
    requestID: String(body.requestID ?? body.requestId ?? '').trim(),
    provider: String(body.provider ?? '').trim(),
    action: String(body.action ?? '').trim(),
    actionVersion: String(body.actionVersion ?? '').trim(),
    resourceRef: stableResourceRef(body.resourceRef),
  };
  const hasResult = Object.prototype.hasOwnProperty.call(body, 'result');
  const hasError = Object.prototype.hasOwnProperty.call(body, 'error') && body.error !== undefined && body.error !== null;
  if (!envelope.requestID || !envelope.provider || !envelope.action || !envelope.actionVersion || !envelope.resourceRef) {
    return undefined;
  }
  if (hasResult === hasError) return undefined;
  if (hasError) {
    if (typeof body.error !== 'object' || Array.isArray(body.error)) return undefined;
    const error = {
      code: String(body.error.code ?? '').trim(),
      message: String(body.error.message ?? '').trim(),
      retryable: body.error.retryable === true,
    };
    if (!error.code || !error.message || typeof body.error.retryable !== 'boolean') return undefined;
    envelope.error = error;
  } else {
    envelope.result = body.result;
  }
  return envelope;
}

function errorFromEnvelope(envelope, status, body) {
  return new ProviderActionError(envelope.error.message, {
    code: envelope.error.code,
    status,
    requestID: envelope.requestID,
    provider: envelope.provider,
    action: envelope.action,
    actionVersion: envelope.actionVersion,
    resourceRef: envelope.resourceRef,
    retryable: envelope.error.retryable,
    failureKind: failureKindForStatus(status) ?? 'upstream',
    body,
  });
}

function failureKindForCode(code) {
  if (['invalid_config', 'invalid_request', 'server_only'].includes(code)) return 'configuration';
  if (String(code).startsWith('credential_')) return 'credentials';
  if (code === 'route_not_found') return 'route';
  if (code === 'authentication_failed') return 'authentication';
  if (code === 'authorization_failed') return 'authorization';
  if (code === 'action_contract_changed') return 'contract';
  if (code === 'upstream_failed') return 'upstream';
  if (['network_error', 'timeout', 'aborted'].includes(code)) return 'network';
  return 'request';
}

function failureKindForStatus(status) {
  if (status === 401) return 'authentication';
  if (status === 403) return 'authorization';
  if (status === 404) return 'route';
  if (status === 409) return 'contract';
  if (status >= 500) return 'upstream';
  return undefined;
}

function httpError(status, body) {
  const message = body && typeof body === 'object' ? String(body.message ?? body.error ?? body.reason ?? '') : '';
  let failureKind = failureKindForStatus(status) ?? 'request';
  let code = 'provider_action_http_error';
  if (failureKind === 'authentication') {
    code = 'authentication_failed';
  } else if (failureKind === 'authorization') {
    code = 'authorization_failed';
  } else if (failureKind === 'route') {
    code = 'route_not_found';
  } else if (failureKind === 'upstream') {
    code = 'upstream_failed';
  } else if (failureKind === 'contract') {
    code = 'action_contract_changed';
  }
  return new ActionsClientError(message || `provider action failed with HTTP ${status}`, {
    code, status, body, retryable: status >= 500, failureKind,
  });
}

export class ActionsClient {
  constructor(options = {}) {
    assertServerOnly();
    this.baseURL = validateBaseURL(
      options.baseURL ?? options.baseUrl ?? process.env.RAILGRID_ACTIONS_BASE_URL,
      options.allowInsecureLoopback === true,
    );
    this.fetch = options.fetch ?? globalThis.fetch;
    if (typeof this.fetch !== 'function') throw new ActionsClientError('fetch is required', { code: 'invalid_config' });
    this.options = {
      ...options,
      baseURL: this.baseURL,
    };
  }

  integration(alias) {
    const name = String(alias ?? '').trim();
    if (!/^[A-Za-z0-9_-]{1,63}$/.test(name)) {
      throw new ActionsClientError('integration alias must be a 1-63 character identifier', {
        code: 'invalid_request', failureKind: 'configuration',
      });
    }
    return {
      invoke: (action, input = {}, requestOptions = {}) => this.invoke(name, action, input, requestOptions),
      invokeEnvelope: (action, input = {}, requestOptions = {}) => this.invokeEnvelope(name, action, input, requestOptions),
    };
  }

  async invoke(alias, action, input = {}, requestOptions = {}) {
    const envelope = await this.invokeEnvelope(alias, action, input, requestOptions);
    return envelope.result;
  }

  async invokeEnvelope(alias, action, input = {}, requestOptions = {}) {
    assertServerOnly();
    const integration = String(alias ?? '').trim();
    if (!/^[A-Za-z0-9_-]{1,63}$/.test(integration)) {
      throw new ActionsClientError('integration alias must be a 1-63 character identifier', {
        code: 'invalid_request', failureKind: 'configuration',
      });
    }
    const actionName = String(action ?? '').trim();
    if (!actionName) throw new ActionsClientError('action is required', { code: 'invalid_request', failureKind: 'configuration' });
    const parsedAction = parseActionID(actionName);
    if (input === undefined || input === null) input = {};
    if (typeof input !== 'object') {
      throw new ActionsClientError('action input must be an object, array, or null', { code: 'invalid_request' });
    }

    const { options, headers: customHeaders } = requestHeaderOptions(this.options, requestOptions);
    const timeoutMs = numberOption(options.timeoutMs, undefined);
    const { signal, cleanup, timedOut } = composeSignal(options.signal, timeoutMs);
    const path = actionPath(integration);
    let token;
    try {
      for (let attempt = 0; attempt < 2; attempt += 1) {
        token = await resolveCredential(options, { forceRefresh: attempt > 0, signal });
        const headers = {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          ...customHeaders,
          Authorization: token,
        };
        let response;
        let body;
        try {
          response = await this.fetch(joinURL(this.baseURL, path), {
            method: 'POST',
            headers,
            body: JSON.stringify({ action: parsedAction.name, actionVersion: parsedAction.version, input }),
            signal,
            redirect: 'error',
          });
          const text = await response.text();
          body = decodeJSON(text);
        } catch (error) {
          if (timedOut()) {
            throw new ActionsClientError('provider action request timed out', { code: 'timeout', retryable: true, cause: error });
          }
          if (isAbortError(error) || signal?.aborted) {
            throw new ActionsClientError('provider action request was aborted', { code: 'aborted', retryable: true, cause: error });
          }
          throw new ActionsClientError('provider action request failed', { code: 'network_error', retryable: true, cause: error });
        }

        if (response.status === 401 && attempt === 0 && hasRefreshableCredential(options)) continue;
        const envelope = stableEnvelope(body);
        if (!envelope) {
          if (!response.ok) throw httpError(response.status, body);
          throw new ActionsClientError('provider action response did not match the stable envelope', {
            code: 'invalid_response', status: response.status, body,
          });
        }
        if (envelope.error) throw errorFromEnvelope(envelope, response.status, body);
        if (!response.ok) throw httpError(response.status, body);
        return envelope;
      }
      throw new ActionsClientError('provider action authentication failed', { code: 'authentication_failed', status: 401 });
    } finally {
      cleanup();
    }
  }

}

export function createActionsClient(options) {
  return new ActionsClient(options);
}

export default createActionsClient;
