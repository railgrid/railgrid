/**
 * Server-side client for App Studio Provider Actions.
 *
 * The package deliberately models the stable gateway envelope rather than a
 * provider-specific response.  `resourceRef` is selected by the App Studio
 * project binding; callers may only supply action input.
 */

export interface ProviderResourceReference {
  name: string;
  apiVersion: string;
  kind: string;
  resource: string;
}

export interface ProviderActionErrorEnvelope {
  code: string;
  message: string;
  retryable: boolean;
}

export interface ProviderActionSuccessEnvelope<TResult = unknown> {
  requestID: string;
  provider: string;
  action: string;
  actionVersion: string;
  resourceRef: ProviderResourceReference;
  result: TResult;
  error?: never;
}

export interface ProviderActionFailureEnvelope {
  requestID: string;
  provider: string;
  action: string;
  actionVersion: string;
  resourceRef: ProviderResourceReference;
  result?: never;
  error: ProviderActionErrorEnvelope;
}

export type ProviderActionEnvelope<TResult = unknown> =
  | ProviderActionSuccessEnvelope<TResult>
  | ProviderActionFailureEnvelope;

export interface CredentialContext {
  /** True when the previous request received 401 and a fresh token is needed. */
  forceRefresh: boolean;
  signal?: AbortSignal;
}

export type CredentialProvider =
  (context: CredentialContext) => string | null | undefined | Promise<string | null | undefined>;

export interface CredentialProviderObject {
  getToken: CredentialProvider;
}

export interface ActionsRequestOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
  idempotencyKey?: string;
  requestID?: string;
  requestId?: string;
  correlationID?: string;
  correlationId?: string;
  actionDeadlineMs?: number | string;
  deadlineMs?: number | string;
  headers?: Record<string, string>;
}

export interface ActionsClientOptions extends ActionsRequestOptions {
  /** URL of the authenticated App Studio service, not a provider backend URL. */
  baseURL?: string;
  baseUrl?: string;
  /** Defaults to KEDGE_PROJECT. */
  project?: string;
  /** Defaults to KEDGE_ACTIONS_ORG and KEDGE_ACTIONS_WORKSPACE. */
  org?: string;
  organization?: string;
  workspace?: string;
  /** Test/local-only escape hatch for HTTP loopback URLs. */
  allowInsecureLoopback?: boolean;
  token?: string | CredentialProvider;
  /** Read the atomically refreshed bearer token on every request. Defaults to KEDGE_ACTIONS_TOKEN_FILE. */
  tokenFile?: string;
  getToken?: CredentialProvider;
  credentialProvider?: CredentialProvider | CredentialProviderObject;
  fetch?: typeof globalThis.fetch;
}

export interface ActionsIntegration {
  invoke<TResult = unknown>(
    action: string,
    input?: unknown,
    options?: ActionsRequestOptions,
  ): Promise<TResult>;
  invokeEnvelope<TResult = unknown>(
    action: string,
    input?: unknown,
    options?: ActionsRequestOptions,
  ): Promise<ProviderActionSuccessEnvelope<TResult>>;
}

export interface ActionsClientErrorOptions {
  name?: string;
  code?: string;
  status?: number;
  requestID?: string;
  requestId?: string;
  provider?: string;
  action?: string;
  actionVersion?: string;
  resourceRef?: ProviderResourceReference;
  retryable?: boolean;
  body?: unknown;
  cause?: unknown;
}

export class ActionsClientError extends Error {
  readonly name: string;
  readonly code: string;
  readonly status: number;
  readonly requestID: string;
  readonly provider: string;
  readonly action: string;
  readonly actionVersion: string;
  readonly resourceRef?: ProviderResourceReference;
  readonly retryable: boolean;
  readonly body?: unknown;
  readonly cause?: unknown;

  constructor(message?: string, options?: ActionsClientErrorOptions);
}

export class ProviderActionError extends ActionsClientError {
  constructor(message?: string, options?: ActionsClientErrorOptions);
}

export class ActionsClient {
  readonly baseURL?: string;
  readonly project: string;
  readonly org: string;
  readonly workspace: string;

  constructor(options: ActionsClientOptions);

  integration(alias: string): ActionsIntegration;
  invoke<TResult = unknown>(
    alias: string,
    action: string,
    input?: unknown,
    options?: ActionsRequestOptions,
  ): Promise<TResult>;
  invokeEnvelope<TResult = unknown>(
    alias: string,
    action: string,
    input?: unknown,
    options?: ActionsRequestOptions,
  ): Promise<ProviderActionSuccessEnvelope<TResult>>;
}

export function createActionsClient(options: ActionsClientOptions): ActionsClient;

export default createActionsClient;
