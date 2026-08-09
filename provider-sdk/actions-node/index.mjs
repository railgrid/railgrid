import { readFile } from 'node:fs/promises';

/**
 * Server-side App Studio Provider Actions client.
 *
 * The SDK only talks to the App Studio gateway. It never accepts a provider
 * URL, provider credential, or backend topology and must remain in a server
 * process because the caller credential is sent in the Authorization header.
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
      'The Kedge Actions SDK is server-only; never expose a caller credential to a browser',
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
  if (parsed.protocol === 'https:') return value;
  if (parsed.protocol === 'http:' && allowInsecureLoopback === true && isLoopbackHost(parsed.hostname)) return value;
  throw new ActionsClientError('baseURL must use HTTPS (or explicitly allow HTTP loopback for local tests)', { code: 'invalid_config' });
}

function actionPath(project, alias) {
  return `/api/projects/${encodeURIComponent(project)}/integrations/${encodeURIComponent(alias)}/invoke`;
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
  const configured = options.tokenFile ?? process.env.KEDGE_ACTIONS_TOKEN_FILE;
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
      throw new ActionsClientError('the Kedge caller credential file is unavailable', {
        code: 'credential_file_unavailable',
        retryable: !forceRefresh,
        cause: error,
      });
    }
    const normalized = normalizeToken(contents);
    if (normalized) return normalized;
    throw new ActionsClientError('the Kedge caller credential file is empty', {
      code: 'credential_file_unavailable',
      retryable: !forceRefresh,
    });
  }
  throw new ActionsClientError(
    'a Kedge caller credential is required; pass token, tokenFile, getToken, or credentialProvider on the server',
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
  if (deadline !== undefined) headers['X-Kedge-Action-Deadline-Ms'] = String(deadline);
  const org = options.org ?? options.organization ?? process.env.KEDGE_ACTIONS_ORG;
  if (org !== undefined && String(org).trim() !== '') headers['X-Kedge-Org'] = String(org).trim();
  const workspace = options.workspace ?? process.env.KEDGE_ACTIONS_WORKSPACE;
  if (workspace !== undefined && String(workspace).trim() !== '') headers['X-Kedge-Workspace'] = String(workspace).trim();
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
    body,
  });
}

function httpError(status, body) {
  const message = body && typeof body === 'object' ? String(body.message ?? body.error ?? '') : '';
  return new ActionsClientError(message || `provider action failed with HTTP ${status}`, {
    code: 'provider_action_http_error', status, body, retryable: status >= 500,
  });
}

export class ActionsClient {
  constructor(options = {}) {
    assertServerOnly();
    this.baseURL = validateBaseURL(
      options.baseURL ?? options.baseUrl ?? process.env.KEDGE_ACTIONS_BASE_URL,
      options.allowInsecureLoopback === true,
    );
    this.project = String(options.project ?? process.env.KEDGE_PROJECT ?? '').trim();
    if (!this.project) throw new ActionsClientError('project is required', { code: 'invalid_config' });
    this.fetch = options.fetch ?? globalThis.fetch;
    if (typeof this.fetch !== 'function') throw new ActionsClientError('fetch is required', { code: 'invalid_config' });
    this.org = String(options.org ?? options.organization ?? process.env.KEDGE_ACTIONS_ORG ?? '').trim();
    this.workspace = String(options.workspace ?? process.env.KEDGE_ACTIONS_WORKSPACE ?? '').trim();
    this.options = {
      ...options,
      baseURL: this.baseURL,
      project: this.project,
      org: this.org,
      workspace: this.workspace,
    };
  }

  integration(alias) {
    const name = String(alias ?? '').trim();
    if (!name) throw new ActionsClientError('integration alias is required', { code: 'invalid_request' });
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
    if (!integration) throw new ActionsClientError('integration alias is required', { code: 'invalid_request' });
    const actionName = String(action ?? '').trim();
    if (!actionName) throw new ActionsClientError('action is required', { code: 'invalid_request' });
    if (input === undefined || input === null) input = {};
    if (typeof input !== 'object') {
      throw new ActionsClientError('action input must be an object, array, or null', { code: 'invalid_request' });
    }

    const { options, headers: customHeaders } = requestHeaderOptions(this.options, requestOptions);
    const timeoutMs = numberOption(options.timeoutMs, undefined);
    const { signal, cleanup, timedOut } = composeSignal(options.signal, timeoutMs);
    const path = actionPath(this.project, integration);
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
            body: JSON.stringify({ action: actionName, input }),
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
