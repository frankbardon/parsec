/**
 * Public re-exports. No logic.
 */
export { ParsecClient } from "./client.js";
export type { ParsecClientOptions, TypedSubscription } from "./client.js";

export { ParsecError, ParsecErrorCode, isRecoverable, mapTwirpError } from "./errors.js";
export type { TwirpErrorBody } from "./errors.js";

export {
  parseName,
  buildName,
  formatName,
  isPrivate,
  Visibility,
} from "./channels.js";
export type { Name } from "./channels.js";

export { fetchManifest, pickTransports, transportEndpoint } from "./manifest.js";
export type { FetchManifestOptions } from "./manifest.js";

export { MemoryTokenStore } from "./tokens.js";
export type { TokenPair, TokenStore } from "./tokens.js";

export { decodeJwtPayload, inspectScopes } from "./scopes.js";
export type { ScopeInspection } from "./scopes.js";

export { ParsecVerb } from "./types.js";
export type {
  Claims,
  Envelope,
  Manifest,
  RefreshTokenRequest,
  RefreshTokenResponse,
  Scope,
  TransportName,
} from "./types.js";
