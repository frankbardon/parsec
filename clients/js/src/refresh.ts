/**
 * Refresh-token state machine. Single-flight: concurrent calls share one
 * in-flight RPC. Preemptive timer fires at `accessExp − skew − jitter` so
 * centrifuge-js's getToken callback is only the safety net.
 */
import { ParsecError, ParsecErrorCode } from "./errors.js";
import type { TokenStore, TokenPair } from "./tokens.js";
import { twirpCall, type TwirpCallOptions } from "./twirp.js";
import type { RefreshTokenRequest, RefreshTokenResponse } from "./types.js";

export interface RefreshOptions {
  /** Base URL of the parsec server (e.g. https://parsec.example.com). */
  endpoint: string;
  /** Token storage. Owns rotation persistence. */
  store: TokenStore;
  /** Skew in seconds — refresh is forced when accessExp is within this window. */
  skewSeconds?: number;
  /** Optional fetch override for tests. */
  fetch?: typeof fetch;
  /** Called when refresh fails terminally (auth denied/expired). */
  onTerminal?: (err: ParsecError) => void;
  /** Optional clock for tests. */
  now?: () => number;
}

type State =
  | { kind: "idle" }
  | { kind: "refreshing"; promise: Promise<TokenPair> };

/**
 * RefreshController owns the refresh state. Construct one per ParsecClient.
 */
export class RefreshController {
  private state: State = { kind: "idle" };
  private timer: ReturnType<typeof setTimeout> | null = null;
  private disposed = false;

  constructor(private readonly opts: RefreshOptions) {}

  /**
   * Returns a valid access token, refreshing when necessary. Single-flight:
   * concurrent calls await the same in-flight promise.
   */
  async ensureFresh(): Promise<string> {
    if (this.disposed) {
      throw new ParsecError(ParsecErrorCode.Internal, "refresh controller disposed");
    }
    const pair = await Promise.resolve(this.opts.store.get());
    const now = this.now();
    const skew = (this.opts.skewSeconds ?? 30) * 1000;
    if (pair && pair.accessExp * 1000 - now > skew) {
      return pair.access;
    }
    return (await this.runRefresh()).access;
  }

  /**
   * Force a refresh now regardless of the current token TTL. Used by the
   * preemptive timer.
   */
  async refreshNow(): Promise<TokenPair> {
    return this.runRefresh();
  }

  /**
   * Schedule the preemptive timer based on the currently-stored token. Call
   * after every successful refresh.
   */
  async schedulePreemptive(): Promise<void> {
    if (this.disposed) return;
    if (this.timer) clearTimeout(this.timer);
    const pair = await Promise.resolve(this.opts.store.get());
    if (!pair || pair.accessExp === 0) return;
    const skew = (this.opts.skewSeconds ?? 30) * 1000;
    // jitter so a fleet does not refresh in lockstep
    const jitter = Math.floor(Math.random() * 5000);
    const fireIn = pair.accessExp * 1000 - this.now() - skew - jitter;
    if (fireIn <= 0) return;
    this.timer = setTimeout(() => {
      // Best-effort; failures surface via onTerminal or are retried on the
      // next ensureFresh call.
      this.runRefresh().catch(() => undefined);
    }, fireIn);
  }

  /**
   * Stop the preemptive timer and refuse further calls. Idempotent.
   */
  dispose(): void {
    this.disposed = true;
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private async runRefresh(): Promise<TokenPair> {
    if (this.state.kind === "refreshing") {
      return this.state.promise;
    }
    const promise = this.executeRefresh();
    this.state = { kind: "refreshing", promise };
    try {
      const pair = await promise;
      return pair;
    } finally {
      this.state = { kind: "idle" };
    }
  }

  private async executeRefresh(): Promise<TokenPair> {
    const current = await Promise.resolve(this.opts.store.get());
    if (!current || current.refresh === "") {
      const err = new ParsecError(ParsecErrorCode.AuthExpired, "no refresh token available");
      this.fireTerminal(err);
      throw err;
    }
    const callOpts: TwirpCallOptions = {};
    if (this.opts.fetch) callOpts.fetch = this.opts.fetch;
    let resp: RefreshTokenResponse;
    try {
      resp = await twirpCall<RefreshTokenRequest, RefreshTokenResponse>(
        this.opts.endpoint,
        "RefreshToken",
        { refreshToken: current.refresh },
        callOpts,
      );
    } catch (err) {
      if (err instanceof ParsecError && isTerminal(err.code)) {
        await Promise.resolve(this.opts.store.clear());
        this.fireTerminal(err);
      }
      throw err;
    }
    const next: TokenPair = {
      access: resp.accessToken,
      accessExp: resp.accessExpiresUnix,
      // The response refresh is canonical: legacy tokens (rotated=false)
      // may omit it, in which case we keep the old one.
      refresh: resp.refreshToken && resp.refreshToken !== "" ? resp.refreshToken : current.refresh,
      refreshExp:
        resp.refreshExpiresUnix && resp.refreshExpiresUnix > 0
          ? resp.refreshExpiresUnix
          : current.refreshExp,
    };
    // Persist BEFORE returning — an in-flight centrifuge reconnect that
    // reads the store while we are mid-resolve would otherwise reuse the
    // stale refresh and trip family revoke.
    await Promise.resolve(this.opts.store.set(next));
    void this.schedulePreemptive();
    return next;
  }

  private fireTerminal(err: ParsecError): void {
    try {
      this.opts.onTerminal?.(err);
    } catch {
      // user callback errors do not propagate
    }
  }

  private now(): number {
    return this.opts.now ? this.opts.now() : Date.now();
  }
}

function isTerminal(code: string): boolean {
  return code === ParsecErrorCode.AuthDenied || code === ParsecErrorCode.AuthExpired;
}
