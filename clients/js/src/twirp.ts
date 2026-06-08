/**
 * Internal fetch-based Twirp JSON caller. Used by the refresh module; not
 * exported from the public surface. Twirp's wire conventions:
 *
 * - HTTP POST to /twirp/<service>/<method>
 * - Content-Type: application/json on both directions
 * - Error responses carry a JSON body { code: "<twirp_code>", msg: "...", meta? }
 */
import { mapTwirpError, ParsecError, ParsecErrorCode, type TwirpErrorBody } from "./errors.js";

const TWIRP_PATH_PREFIX = "/twirp/parsec.ParsecService/";

export interface TwirpCallOptions {
  /** Optional Authorization header value (e.g. `Bearer <token>`). */
  bearer?: string;
  /** AbortSignal forwarded to the underlying fetch call. */
  signal?: AbortSignal;
  /** Per-call timeout in ms. Default 10_000. */
  timeoutMs?: number;
  /** Allow injection in tests. Falls back to globalThis.fetch. */
  fetch?: typeof fetch;
}

/**
 * Issue a Twirp JSON call. On non-2xx, parses the Twirp error body and
 * throws a ParsecError via mapTwirpError. On network failure, throws
 * ParsecError(BrokerNotReady).
 */
export async function twirpCall<TReq, TResp>(
  baseUrl: string,
  method: string,
  body: TReq,
  opts: TwirpCallOptions = {},
): Promise<TResp> {
  const url = joinUrl(baseUrl, TWIRP_PATH_PREFIX + method);
  const fetchFn = opts.fetch ?? globalThis.fetch;
  if (typeof fetchFn !== "function") {
    throw new ParsecError(ParsecErrorCode.Internal, "fetch is not available in this environment");
  }
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "application/json",
  };
  if (opts.bearer) headers["Authorization"] = `Bearer ${opts.bearer}`;

  const ctrl = new AbortController();
  const timeoutMs = opts.timeoutMs ?? 10_000;
  const timeoutId = setTimeout(() => ctrl.abort(), timeoutMs);
  const externalAbort = () => ctrl.abort();
  if (opts.signal) {
    if (opts.signal.aborted) ctrl.abort();
    else opts.signal.addEventListener("abort", externalAbort, { once: true });
  }

  let resp: Response;
  try {
    resp = await fetchFn(url, {
      method: "POST",
      headers,
      body: JSON.stringify(body ?? {}),
      signal: ctrl.signal,
    });
  } catch (cause) {
    clearTimeout(timeoutId);
    opts.signal?.removeEventListener("abort", externalAbort);
    throw new ParsecError(ParsecErrorCode.BrokerNotReady, "twirp: network error", { cause });
  } finally {
    clearTimeout(timeoutId);
    opts.signal?.removeEventListener("abort", externalAbort);
  }

  if (!resp.ok) {
    let body: TwirpErrorBody | null = null;
    try {
      body = (await resp.json()) as TwirpErrorBody;
    } catch {
      // body wasn't JSON; synthesize a code from HTTP status
    }
    if (body && typeof body.code === "string") {
      throw mapTwirpError(body);
    }
    throw new ParsecError(
      httpStatusToCode(resp.status),
      `twirp: HTTP ${resp.status} ${resp.statusText}`,
    );
  }

  return (await resp.json()) as TResp;
}

function httpStatusToCode(status: number): ParsecErrorCode {
  if (status === 401 || status === 403) return ParsecErrorCode.AuthDenied;
  if (status === 404) return ParsecErrorCode.ChannelNotFound;
  if (status === 429) return ParsecErrorCode.RateLimited;
  if (status >= 500) return ParsecErrorCode.BrokerNotReady;
  return ParsecErrorCode.Internal;
}

function joinUrl(base: string, path: string): string {
  const trimmedBase = base.endsWith("/") ? base.slice(0, -1) : base;
  const trimmedPath = path.startsWith("/") ? path : `/${path}`;
  return trimmedBase + trimmedPath;
}
