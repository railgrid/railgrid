export interface RailgridUser {
  /** Opaque, stable Railgrid subject identifier. Do not replace it with email. */
  id: string;
  /** Optional display label. It is not an account key. */
  displayName?: string;
}

export interface RailgridRequest {
  method?: string;
  headers?: Record<string, string | string[] | undefined>;
}

export interface RailgridResponse {
  statusCode?: number;
  setHeader(name: string, value: string): void;
  end(body?: string): void;
  status?(status: number): RailgridResponse;
  json?(body: unknown): void;
  type?(contentType: string): RailgridResponse;
}

export type RailgridNext = (error?: unknown) => void;
export type RailgridMiddleware = (
  request: RailgridRequest,
  response: RailgridResponse,
  next: RailgridNext,
) => void;

export interface RailgridAuth {
  /**
   * Verify the identity injected by the trusted Railgrid application proxy.
   * The request must carry the `X-Railgrid-App-Identity` header. Unsafe HTTP
   * methods also require an exact match for the configured Origin.
  */
  getUser(request: RailgridRequest): Promise<RailgridUser>;
  /**
   * Return null only when the Railgrid identity header is absent. A malformed,
   * forged, expired, or otherwise invalid assertion still rejects with 401.
   */
  getOptionalUser(request: RailgridRequest): Promise<RailgridUser | null>;
  /** Express/Connect-compatible middleware that sends structured errors. */
  middleware(): RailgridMiddleware;
}

export interface RailgridAuthOperationalOptions {
  /** Fetch injection for tests and custom Node runtimes. The URL is fixed by the issuer. */
  fetch?: typeof fetch;
  /** Clock injection for deterministic tests; the value is Unix milliseconds. */
  now?: () => number;
  /** JWKS cache duration in milliseconds, capped at five minutes. */
  cacheTTLMs?: number;
}

export class RailgridAuthError extends Error {
  readonly code: string;
  readonly status: number;
  toJSON(): { error: { code: string; message: string } };
}

/** Create a server-only verifier using the trusted RAILGRID_AUTH_* environment. */
export function createRailgridAuth(options?: RailgridAuthOperationalOptions): RailgridAuth;
