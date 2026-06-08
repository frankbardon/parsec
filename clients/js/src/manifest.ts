/**
 * Manifest fetch + transport ranking. The manifest is the Parsec server's
 * self-describing capability list at GET /manifest. Reading it lets the
 * client know which transports are actually mounted and the operator's
 * scope grammar so SDK callers don't hard-code assumptions.
 */
import { ParsecError, ParsecErrorCode } from "./errors.js";
import type { Envelope, Manifest, TransportName } from "./types.js";

export interface FetchManifestOptions {
  /** Optional fetch override for tests. */
  fetch?: typeof fetch;
  /** Per-call timeout in ms. Default 5_000. */
  timeoutMs?: number;
  /** AbortSignal forwarded to the underlying fetch call. */
  signal?: AbortSignal;
}

/**
 * fetchManifest GETs <baseUrl>/manifest, unwraps the descriptor.Envelope,
 * and returns the Manifest payload. Throws ParsecError on non-2xx or on
 * envelope shape mismatch.
 */
export async function fetchManifest(
  baseUrl: string,
  opts: FetchManifestOptions = {},
): Promise<Manifest> {
  const fetchFn = opts.fetch ?? globalThis.fetch;
  if (typeof fetchFn !== "function") {
    throw new ParsecError(ParsecErrorCode.Internal, "fetch is not available in this environment");
  }
  const url = joinUrl(baseUrl, "/manifest");
  const ctrl = new AbortController();
  const timeoutMs = opts.timeoutMs ?? 5_000;
  const timeoutId = setTimeout(() => ctrl.abort(), timeoutMs);
  const externalAbort = () => ctrl.abort();
  if (opts.signal) {
    if (opts.signal.aborted) ctrl.abort();
    else opts.signal.addEventListener("abort", externalAbort, { once: true });
  }
  let resp: Response;
  try {
    resp = await fetchFn(url, {
      method: "GET",
      headers: { Accept: "application/json" },
      signal: ctrl.signal,
    });
  } catch (cause) {
    throw new ParsecError(ParsecErrorCode.BrokerNotReady, "manifest: network error", { cause });
  } finally {
    clearTimeout(timeoutId);
    opts.signal?.removeEventListener("abort", externalAbort);
  }
  if (!resp.ok) {
    throw new ParsecError(
      ParsecErrorCode.BrokerNotReady,
      `manifest: HTTP ${resp.status} ${resp.statusText}`,
    );
  }
  let env: Envelope<Manifest>;
  try {
    env = (await resp.json()) as Envelope<Manifest>;
  } catch (cause) {
    throw new ParsecError(ParsecErrorCode.Internal, "manifest: invalid JSON", { cause });
  }
  if (!env || typeof env.format_version !== "string") {
    throw new ParsecError(ParsecErrorCode.Internal, "manifest: missing envelope");
  }
  // The descriptor envelope wraps a typed payload. For Manifest the payload
  // is the Manifest struct directly.
  if (!env.payload || typeof env.payload !== "object") {
    throw new ParsecError(ParsecErrorCode.Internal, "manifest: missing payload");
  }
  return env.payload;
}

/**
 * pickTransports intersects the server's advertised transports with the
 * client's preference list. The CLIENT's order wins (most-preferred first).
 * Returns a copy — never mutates inputs.
 */
export function pickTransports(
  server: readonly TransportName[] | undefined,
  prefer: readonly TransportName[],
): TransportName[] {
  if (!server || server.length === 0) {
    // Server told us nothing — websocket is always mounted, so prefer it.
    return prefer.includes("websocket") ? ["websocket"] : [...prefer];
  }
  const serverSet = new Set(server);
  const picked: TransportName[] = [];
  for (const t of prefer) {
    if (serverSet.has(t)) picked.push(t);
  }
  if (picked.length === 0) {
    // No intersection — degrade to websocket since it is the wire baseline.
    return ["websocket"];
  }
  return picked;
}

/**
 * transportEndpoint builds the URL centrifuge-js expects for a given transport
 * given the server's HTTP(S) base URL. Websocket and webtransport upgrade
 * the scheme; http_stream / sse stay on HTTP(S).
 */
export function transportEndpoint(baseUrl: string, transport: TransportName): string {
  const path = transportPath(transport);
  if (transport === "websocket") {
    return joinUrl(httpToWs(baseUrl), path);
  }
  return joinUrl(baseUrl, path);
}

function transportPath(t: TransportName): string {
  switch (t) {
    case "websocket":
      return "/connection/websocket";
    case "http_stream":
      return "/connection/http_stream";
    case "webtransport":
      return "/connection/webtransport";
    case "sse":
      return "/connection/sse";
  }
}

function httpToWs(url: string): string {
  if (url.startsWith("https://")) return "wss://" + url.slice("https://".length);
  if (url.startsWith("http://")) return "ws://" + url.slice("http://".length);
  return url;
}

function joinUrl(base: string, path: string): string {
  const trimmedBase = base.endsWith("/") ? base.slice(0, -1) : base;
  const trimmedPath = path.startsWith("/") ? path : `/${path}`;
  return trimmedBase + trimmedPath;
}
