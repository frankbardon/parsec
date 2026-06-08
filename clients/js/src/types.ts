/**
 * Shared interfaces. Importable without dragging centrifuge-js into the
 * type graph — useful for callers that only want the scope inspector or
 * descriptor types.
 */

/**
 * Wire shape of the descriptor envelope returned by `/manifest` and other
 * `--json` endpoints. Mirrors `descriptor.Envelope` in
 * `descriptor/manifest.go`.
 */
export interface Envelope<T = unknown> {
  format_version: string;
  kind: string;
  generated_at: string;
  payload: T;
}

/**
 * Subset of `descriptor.Manifest` the client consumes. The Go struct has
 * many more fields; we mirror only the ones the wrapper cares about plus
 * a couple of useful operator labels. Unknown fields are preserved on the
 * raw payload via the index signature.
 */
export interface Manifest {
  service?: string;
  version?: string;
  surfaces?: string[];
  sinks?: string[];
  visibilities?: string[];
  max_private_ttl?: string;
  transports?: TransportName[];
  metrics_enabled?: boolean;
  tracing_enabled?: boolean;
  scope_grammar?: string;
  supported_verbs?: string[];
  deny_supported?: boolean;
  scope_precedence?: string;
  region?: string;
  auth_oidc_enabled?: boolean;
  auth_oidc_issuer?: string;
  token_broker_enabled?: boolean;
  revocation_store_enabled?: boolean;
  cache_enabled?: boolean;
  cache_backend?: string;
  [key: string]: unknown;
}

/**
 * Wire-stable transport name. Mirrors centrifuge-js's TransportName but
 * pinned to the transports Parsec actually serves.
 */
export type TransportName = "websocket" | "http_stream" | "webtransport" | "sse";

/**
 * Verb tokens mirrored from `auth/scope.go`.
 */
export const ParsecVerb = {
  Subscribe: "subscribe",
  Publish: "publish",
  Manage: "manage",
} as const;

export type ParsecVerb = (typeof ParsecVerb)[keyof typeof ParsecVerb];

/**
 * One entry in a token's `scopes` array. Mirrors `auth.Scope`.
 */
export interface Scope {
  pat: string;
  v: ParsecVerb[];
  deny?: boolean;
}

/**
 * Decoded JWT payload. Mirrors `auth.Claims`. Field names match the wire
 * shape (short keys).
 */
export interface Claims {
  sub?: string;
  typ?: "access" | "refresh" | "mgmt";
  chs?: string[];
  iat?: number;
  exp?: number;
  scopes?: Scope[];
  jti?: string;
  fid?: string;
  rl?: unknown;
  [key: string]: unknown;
}

/**
 * RefreshToken RPC request body. Mirrors `rpc.RefreshTokenRequest`.
 */
export interface RefreshTokenRequest {
  refreshToken: string;
}

/**
 * RefreshToken RPC response body. Mirrors `rpc.RefreshTokenResponse`.
 * Field names follow protojson lowerCamelCase.
 */
export interface RefreshTokenResponse {
  accessToken: string;
  accessExpiresUnix: number;
  refreshToken?: string;
  refreshExpiresUnix?: number;
  rotated?: boolean;
}
