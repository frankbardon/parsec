/**
 * Coded error codes mirrored from `errors/codes.go`. Keep in sync with the
 * Go source — any new code there requires a new code here in the same PR.
 */
export const ParsecErrorCode = {
  ChannelInvalid: "PARSEC_CHANNEL_INVALID",
  ChannelNotFound: "PARSEC_CHANNEL_NOT_FOUND",
  ChannelExists: "PARSEC_CHANNEL_EXISTS",
  ChannelClosed: "PARSEC_CHANNEL_CLOSED",
  ChannelTTLExceeds: "PARSEC_CHANNEL_TTL_EXCEEDS_MAX",
  AuthDenied: "PARSEC_AUTH_DENIED",
  AuthExpired: "PARSEC_AUTH_EXPIRED",
  BrokerNotReady: "PARSEC_BROKER_NOT_READY",
  BrokerInternal: "PARSEC_BROKER_INTERNAL",
  SinkUnavailable: "PARSEC_SINK_UNAVAILABLE",
  SinkConfig: "PARSEC_SINK_CONFIG_INVALID",
  SinkDLQOverflow: "PARSEC_SINK_DLQ_OVERFLOW",
  SinkDLQNotFound: "PARSEC_SINK_DLQ_NOT_FOUND",
  RateLimited: "PARSEC_RATE_LIMITED",
  InvalidArgument: "PARSEC_INVALID_ARGUMENT",
  Internal: "PARSEC_INTERNAL",
} as const;

export type ParsecErrorCode = (typeof ParsecErrorCode)[keyof typeof ParsecErrorCode];

/**
 * ParsecError is the typed error every client API surface throws. Holders
 * can branch on `.code` instead of string-matching messages.
 */
export class ParsecError extends Error {
  readonly code: ParsecErrorCode;
  override readonly cause?: unknown;

  constructor(code: ParsecErrorCode, message: string, options?: { cause?: unknown }) {
    super(message);
    this.name = "ParsecError";
    this.code = code;
    if (options?.cause !== undefined) {
      this.cause = options.cause;
    }
  }
}

/**
 * Whether the failure is retryable. Network glitches, rate-limit kicks,
 * and broker-not-ready hiccups are recoverable; auth + malformed-input
 * failures are terminal.
 */
export function isRecoverable(err: unknown): boolean {
  if (!(err instanceof ParsecError)) return true;
  switch (err.code) {
    case ParsecErrorCode.RateLimited:
    case ParsecErrorCode.BrokerNotReady:
    case ParsecErrorCode.BrokerInternal:
    case ParsecErrorCode.Internal:
      return true;
    default:
      return false;
  }
}

/**
 * Twirp JSON error body shape. Surfaced when a Twirp endpoint returns a
 * non-2xx response.
 */
export interface TwirpErrorBody {
  code: string;
  msg: string;
  meta?: Record<string, string>;
}

/**
 * Map a Twirp error body to a ParsecError. Mirrors the inverse mapping in
 * `internal/rpcclient/errors.go`. Unknown Twirp codes pass through as
 * PARSEC_INTERNAL with the body's msg preserved.
 */
export function mapTwirpError(body: TwirpErrorBody): ParsecError {
  const msg = body.msg ?? body.code;
  switch (body.code) {
    case "invalid_argument":
      return new ParsecError(ParsecErrorCode.InvalidArgument, msg);
    case "not_found":
      return new ParsecError(ParsecErrorCode.ChannelNotFound, msg);
    case "already_exists":
      return new ParsecError(ParsecErrorCode.ChannelExists, msg);
    case "failed_precondition":
      return new ParsecError(ParsecErrorCode.ChannelClosed, msg);
    case "permission_denied":
      return new ParsecError(ParsecErrorCode.AuthDenied, msg);
    case "unauthenticated":
      return new ParsecError(ParsecErrorCode.AuthExpired, msg);
    case "unavailable":
      return new ParsecError(ParsecErrorCode.BrokerNotReady, msg);
    case "resource_exhausted":
      return new ParsecError(ParsecErrorCode.RateLimited, msg);
    default:
      return new ParsecError(ParsecErrorCode.Internal, msg, { cause: body });
  }
}
