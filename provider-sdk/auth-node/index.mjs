import { createPublicKey, verify as verifySignature } from 'node:crypto';

const IDENTITY_HEADER = 'x-railgrid-app-identity';
const DEFAULT_CACHE_TTL_MS = 60_000;
const MAX_CACHE_TTL_MS = 5 * 60_000;
const MAX_JWKS_BYTES = 64 * 1024;
const MAX_JWKS_KEYS = 64;
const MAX_TOKEN_BYTES = 16 * 1024;
const MAX_TOKEN_LIFETIME_SECONDS = 15 * 60;
// A cached key may still validate an assertion minted just before JWKS cache
// expiry. Keep it only long enough for that bounded assertion to expire.
const MAX_STALE_MS = MAX_TOKEN_LIFETIME_SECONDS * 1_000;
const MAX_CLOCK_SKEW_SECONDS = 30;
const FETCH_TIMEOUT_MS = 5_000;

export class RailgridAuthError extends Error {
  constructor(code, message, status = 401, options = {}) {
    super(message, options);
    this.name = 'RailgridAuthError';
    this.code = code;
    this.status = status;
  }

  toJSON() {
    return { error: { code: this.code, message: this.message } };
  }
}

function failConfiguration(message) {
  return new RailgridAuthError('configuration_error', message, 500);
}

function validateIssuer(raw) {
  const value = String(raw ?? '').trim();
  if (!value) throw failConfiguration('RAILGRID_AUTH_ISSUER is required');
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw failConfiguration('RAILGRID_AUTH_ISSUER must be an absolute HTTPS URL');
  }
  const pathSegments = parsed.pathname.split('/').filter(Boolean);
  if (
    parsed.protocol !== 'https:' || !parsed.host || parsed.username || parsed.password
    || parsed.search || parsed.hash || parsed.pathname === '/' || parsed.pathname.endsWith('/')
    || value.includes('\\') || value.includes('%') || pathSegments.some((segment) => segment === '.' || segment === '..')
    || value !== `${parsed.origin}${parsed.pathname}`
  ) {
    throw failConfiguration('RAILGRID_AUTH_ISSUER must be a canonical HTTPS issuer URL without credentials, query, fragment, or trailing slash');
  }
  return value;
}

function validateAudience(raw) {
  const value = String(raw ?? '').trim();
  if (!value) throw failConfiguration('RAILGRID_AUTH_AUDIENCE is required');
  const parts = value.split('/');
  if (value.length > 512 || /[\u0000-\u0020\u007f]/.test(value) || parts.length !== 2 || parts.some((part) => part === '')) {
    throw failConfiguration('RAILGRID_AUTH_AUDIENCE must use the <ProjectUID>/<environment> form');
  }
  return value;
}

function validateOrigin(raw) {
  const value = String(raw ?? '').trim();
  if (!value) throw failConfiguration('RAILGRID_AUTH_ORIGIN is required');
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw failConfiguration('RAILGRID_AUTH_ORIGIN must be an absolute HTTPS origin');
  }
  if (parsed.protocol !== 'https:' || !parsed.host || parsed.username || parsed.password || parsed.pathname !== '/' || parsed.search || parsed.hash || value !== parsed.origin) {
    throw failConfiguration('RAILGRID_AUTH_ORIGIN must be the exact published HTTPS origin');
  }
  return parsed.origin;
}

function readConfig(env) {
  return Object.freeze({
    issuer: validateIssuer(env.RAILGRID_AUTH_ISSUER),
    audience: validateAudience(env.RAILGRID_AUTH_AUDIENCE),
    origin: validateOrigin(env.RAILGRID_AUTH_ORIGIN),
    instanceUID: validateInstanceUID(env.RAILGRID_AUTH_INSTANCE_UID),
  });
}

function validateInstanceUID(raw) {
  const value = String(raw ?? '').trim();
  if (!requiredString(value, 256)) throw failConfiguration('RAILGRID_AUTH_INSTANCE_UID is required');
  return value;
}

function base64urlJSON(segment) {
  if (!/^[A-Za-z0-9_-]+$/.test(segment)) throw new Error('invalid base64url');
  const decoded = Buffer.from(segment, 'base64url');
  if (decoded.toString('base64url') !== segment) throw new Error('non-canonical base64url');
  return JSON.parse(decoded.toString('utf8'));
}

function record(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function requiredString(value, maximum = 512) {
  return typeof value === 'string' && value.length > 0 && value.length <= maximum && value.trim() !== '' && !/[\u0000-\u001f\u007f]/.test(value);
}

function readToken(request) {
  const value = request?.headers?.[IDENTITY_HEADER];
  if (typeof value !== 'string' || value.length === 0 || value.length > MAX_TOKEN_BYTES) {
    throw new RailgridAuthError('unauthorized', 'Authentication required', 401);
  }
  return value;
}

function isSafeMethod(method) {
  const normalized = String(method ?? '').toUpperCase();
  return normalized === 'GET' || normalized === 'HEAD' || normalized === 'OPTIONS';
}

function enforceOrigin(request, expectedOrigin) {
  if (isSafeMethod(request?.method)) return;
  const origin = request?.headers?.origin;
  if (typeof origin !== 'string' || origin !== expectedOrigin) {
    throw new RailgridAuthError('origin_mismatch', 'Request origin is not allowed', 403);
  }
}

function decodeJWT(token) {
  const parts = token.split('.');
  if (parts.length !== 3 || parts.some((part) => part.length === 0)) {
    throw new RailgridAuthError('unauthorized', 'Authentication required', 401);
  }
  let header;
  let claims;
  let signature;
  try {
    header = base64urlJSON(parts[0]);
    claims = base64urlJSON(parts[1]);
    if (!/^[A-Za-z0-9_-]+$/.test(parts[2])) throw new Error('invalid signature encoding');
    signature = Buffer.from(parts[2], 'base64url');
    if (signature.toString('base64url') !== parts[2]) throw new Error('non-canonical signature encoding');
  } catch {
    throw new RailgridAuthError('unauthorized', 'Authentication required', 401);
  }
  if (!record(header) || !record(claims) || signature.length !== 64) {
    throw new RailgridAuthError('unauthorized', 'Authentication required', 401);
  }
  if (
    Object.keys(header).some((key) => !['alg', 'typ', 'kid'].includes(key))
    || header.alg !== 'EdDSA' || (header.typ !== undefined && header.typ !== 'JWT')
    || !requiredString(header.kid, 256)
  ) {
    throw new RailgridAuthError('unauthorized', 'Authentication required', 401);
  }
  return { header, claims, signature, signingInput: `${parts[0]}.${parts[1]}` };
}

function publicKeyFromJWK(jwk) {
  if (!record(jwk) || jwk.kty !== 'OKP' || jwk.crv !== 'Ed25519' || !requiredString(jwk.kid, 256)) return null;
  if ((jwk.alg !== undefined && jwk.alg !== 'EdDSA') || (jwk.use !== undefined && jwk.use !== 'sig') || jwk.d !== undefined) return null;
  if (typeof jwk.x !== 'string' || !/^[A-Za-z0-9_-]+$/.test(jwk.x)) return null;
  const raw = Buffer.from(jwk.x, 'base64url');
  if (raw.length !== 32 || raw.toString('base64url') !== jwk.x) return null;
  try {
    const key = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: jwk.x }, format: 'jwk' });
    return key.asymmetricKeyType === 'ed25519' ? key : null;
  } catch {
    return null;
  }
}

function parseJWKSet(value) {
  if (!record(value) || !Array.isArray(value.keys) || value.keys.length === 0 || value.keys.length > MAX_JWKS_KEYS) {
    throw new Error('invalid JWKS');
  }
  const keys = new Map();
  for (const jwk of value.keys) {
    const key = publicKeyFromJWK(jwk);
    if (!key) continue;
    if (keys.has(jwk.kid)) throw new Error('duplicate JWKS key ID');
    keys.set(jwk.kid, key);
  }
  if (keys.size === 0) throw new Error('JWKS has no Ed25519 signing keys');
  return keys;
}

async function readBoundedBody(response, limit) {
  const declaredLength = Number(response.headers?.get?.('content-length'));
  if (Number.isFinite(declaredLength) && declaredLength > limit) throw new Error('response exceeded its size limit');
  if (!response.body?.getReader) {
    const text = await response.text();
    if (Buffer.byteLength(text, 'utf8') > limit) throw new Error('response exceeded its size limit');
    return text;
  }

  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) {
        await reader.cancel();
        throw new Error('response exceeded its size limit');
      }
      chunks.push(Buffer.from(value));
    }
  } finally {
    reader.releaseLock();
  }
  return Buffer.concat(chunks, size).toString('utf8');
}

function safeVerificationError() {
  return new RailgridAuthError('unauthorized', 'Authentication required', 401);
}

function validateClaims(claims, config, nowMs) {
  const nowSeconds = Math.floor(nowMs / 1000);
  const expectedEnvironment = config.audience.slice(config.audience.lastIndexOf('/') + 1);
  if (
    claims.iss !== config.issuer || claims.aud !== config.audience
    || claims.railgrid_app_id !== config.audience
    || !requiredString(claims.sub, 512)
    || !requiredString(claims.railgrid_environment, 64)
    || claims.railgrid_environment !== expectedEnvironment
    || claims.railgrid_instance_uid !== config.instanceUID
    || !Number.isSafeInteger(claims.iat) || !Number.isSafeInteger(claims.exp)
    || claims.iat > nowSeconds + MAX_CLOCK_SKEW_SECONDS
    || claims.exp <= nowSeconds
    || claims.exp <= claims.iat
    || claims.exp - claims.iat > MAX_TOKEN_LIFETIME_SECONDS
    || (claims.nbf !== undefined && (!Number.isSafeInteger(claims.nbf) || claims.nbf > nowSeconds + MAX_CLOCK_SKEW_SECONDS))
  ) {
    throw safeVerificationError();
  }
  if (claims.name !== undefined && !requiredString(claims.name, 256)) throw safeVerificationError();
}

function makeUser(claims) {
  const user = { id: claims.sub };
  if (typeof claims.name === 'string' && claims.name.trim()) user.displayName = claims.name;
  return Object.freeze(user);
}

function authFailure(error) {
  if (error instanceof RailgridAuthError) return error;
  return new RailgridAuthError('identity_verification_unavailable', 'Identity verification is temporarily unavailable', 503, { cause: error });
}

function sendFailure(response, error) {
  const body = error.toJSON();
  if (typeof response?.status === 'function' && typeof response?.json === 'function') {
    response.setHeader?.('Cache-Control', 'no-store');
    response.status(error.status).json(body);
    return;
  }
  response.statusCode = error.status;
  response.setHeader('Content-Type', 'application/json; charset=utf-8');
  response.setHeader('Cache-Control', 'no-store');
  response.end(JSON.stringify(body));
}

function validateOperationalOptions(options) {
  const cacheTTLValue = options.cacheTTLMs === undefined ? DEFAULT_CACHE_TTL_MS : Number(options.cacheTTLMs);
  if (!Number.isFinite(cacheTTLValue) || cacheTTLValue < 1_000) {
    throw failConfiguration('cacheTTLMs must be at least one second');
  }
  const cacheTTLMs = Math.min(cacheTTLValue, MAX_CACHE_TTL_MS);
  const fetcher = options.fetch ?? globalThis.fetch;
  if (typeof fetcher !== 'function') throw failConfiguration('the Node.js runtime must provide fetch');
  const now = options.now ?? Date.now;
  if (typeof now !== 'function') throw failConfiguration('now must be a function');
  return { cacheTTLMs, fetcher, now };
}

if (typeof window !== 'undefined' || typeof document !== 'undefined') {
  throw new RailgridAuthError('server_only', 'Railgrid Auth SDK is server-only and cannot be imported by browser code', 500);
}

/** Create a server-only Railgrid identity verifier. */
export function createRailgridAuth(options = {}) {
  if (typeof window !== 'undefined' || typeof document !== 'undefined') {
    throw new RailgridAuthError('server_only', 'Railgrid Auth SDK is server-only and cannot be imported by browser code', 500);
  }
  const config = readConfig(process.env);
  const { cacheTTLMs, fetcher, now } = validateOperationalOptions(options);
  const jwksURL = `${config.issuer}/jwks`;
  const requestUsers = new WeakMap();
  let jwksCache;
  let refreshPromise;
  let unknownKidRefreshUntil = 0;
  let refreshFailureUntil = 0;

  async function fetchJWKS() {
    if (refreshPromise) return refreshPromise;
    if (now() < refreshFailureUntil) {
      throw new RailgridAuthError('identity_verification_unavailable', 'Identity verification is temporarily unavailable', 503);
    }
    const pending = (async () => {
      try {
        const response = await fetcher(jwksURL, {
          method: 'GET',
          headers: { Accept: 'application/json' },
          redirect: 'error',
          credentials: 'omit',
          cache: 'no-store',
          signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
        });
        if (!response?.ok) throw new Error('JWKS endpoint returned an unsuccessful status');
        const keys = parseJWKSet(JSON.parse(await readBoundedBody(response, MAX_JWKS_BYTES)));
        const fetchedAt = now();
        jwksCache = { keys, fetchedAt, expiresAt: fetchedAt + cacheTTLMs };
        return jwksCache;
      } catch (error) {
        refreshFailureUntil = now() + 1_000;
        throw new RailgridAuthError('identity_verification_unavailable', 'Identity verification is temporarily unavailable', 503, { cause: error });
      }
    })();
    refreshPromise = pending;
    try {
      return await pending;
    } finally {
      if (refreshPromise === pending) refreshPromise = undefined;
    }
  }

  async function keyFor(kid) {
    const current = now();
    if (jwksCache?.keys.has(kid) && current < jwksCache.expiresAt) return jwksCache.keys.get(kid);
    if (jwksCache && current < jwksCache.expiresAt && current < unknownKidRefreshUntil) throw safeVerificationError();

    let refreshed;
    try {
      // One request performs at most one network refresh, including for an unknown kid.
      refreshed = await fetchJWKS();
    } catch (error) {
      if (jwksCache?.keys.has(kid) && now() <= jwksCache.expiresAt + MAX_STALE_MS) return jwksCache.keys.get(kid);
      if (jwksCache && current < jwksCache.expiresAt && !jwksCache.keys.has(kid)) unknownKidRefreshUntil = current + 1_000;
      throw error;
    }
    const key = refreshed.keys.get(kid);
    if (!key) {
      unknownKidRefreshUntil = Math.min(refreshed.expiresAt, now() + 1_000);
      throw safeVerificationError();
    }
    return key;
  }

  async function verifyRequest(request) {
    const token = readToken(request);
    enforceOrigin(request, config.origin);
    const { header, claims, signature, signingInput } = decodeJWT(token);
    // Reject obviously wrong app/time claims before reaching the fixed key
    // endpoint. Signature verification still precedes returning any identity.
    validateClaims(claims, config, now());
    const key = await keyFor(header.kid);
    let valid = false;
    try {
      valid = verifySignature(null, Buffer.from(signingInput), key, signature);
    } catch {
      valid = false;
    }
    if (!valid) throw safeVerificationError();
    return makeUser(claims);
  }

  async function getUser(request) {
    if (!request || (typeof request !== 'object' && typeof request !== 'function')) {
      throw safeVerificationError();
    }
    let pending = requestUsers.get(request);
    if (!pending) {
      pending = verifyRequest(request);
      requestUsers.set(request, pending);
      pending.catch(() => requestUsers.delete(request));
    }
    return pending;
  }

  async function getOptionalUser(request) {
    if (!request || (typeof request !== 'object' && typeof request !== 'function')) {
      throw safeVerificationError();
    }
    if (request.headers?.[IDENTITY_HEADER] === undefined) return null;
    return getUser(request);
  }

  function middleware() {
    return function railgridAuthMiddleware(request, response, next) {
      getUser(request).then(
        () => next(),
        (error) => sendFailure(response, authFailure(error)),
      );
    };
  }

  return Object.freeze({ getUser, getOptionalUser, middleware });
}
