/**
 * ParsecClient — composition wrapper around centrifuge-js. Owns the token
 * store, refresh state machine, manifest cache, and channel-name validation.
 * Every method either delegates to the inner Centrifuge instance or sits in
 * front of it to translate Parsec conventions into centrifuge-js inputs.
 */
import {
  Centrifuge,
  type Subscription,
  type SubscriptionOptions,
  type Options as CentrifugeOptions,
  type PublicationContext,
  type TransportEndpoint,
} from "centrifuge";

import { parseName } from "./channels.js";
import { ParsecError, ParsecErrorCode } from "./errors.js";
import {
  fetchManifest,
  pickTransports,
  transportEndpoint,
  type FetchManifestOptions,
} from "./manifest.js";
import { RefreshController } from "./refresh.js";
import { MemoryTokenStore, type TokenStore } from "./tokens.js";
import type { Manifest, TransportName } from "./types.js";

const DEFAULT_TRANSPORTS: TransportName[] = ["websocket", "http_stream"];

export interface ParsecClientOptions {
  /** Base HTTP URL of the parsec server (e.g. https://parsec.example.com). */
  endpoint: string;
  /** Initial access token. */
  accessToken?: string;
  /** Initial refresh token. */
  refreshToken?: string;
  /** Access token expiry (unix seconds). 0 forces an immediate refresh. */
  accessExp?: number;
  /** Refresh token expiry (unix seconds). 0 means unknown. */
  refreshExp?: number;
  /** Pluggable token storage. Defaults to MemoryTokenStore. */
  tokenStore?: TokenStore;
  /** Transport preference order. Defaults to ["websocket", "http_stream"]. */
  preferTransports?: TransportName[];
  /** Whether to fetch the manifest on connect(). Default true. */
  manifest?: boolean;
  /** Refresh skew in seconds. Default 30. */
  refreshSkewSeconds?: number;
  /** Optional centrifuge-js options override (merged with the wrapper's choices). */
  centrifugeOptions?: Partial<CentrifugeOptions>;
  /** Optional fetch override for tests. */
  fetch?: typeof fetch;
}

/**
 * TypedSubscription is a phantom-typed Subscription whose publication
 * data is narrowed to T. The runtime type is the bare Subscription; the
 * narrowing is a contract with whatever schema the channel emits.
 */
export type TypedSubscription<T> = Omit<Subscription, "on"> & {
  on(event: "publication", handler: (ctx: PublicationContext & { data: T }) => void): TypedSubscription<T>;
  on(event: string, handler: (...args: any[]) => void): TypedSubscription<T>;
};

type ClientEvent = "terminal" | "manifestUnavailable";
type EventHandler = (...args: any[]) => void;

export class ParsecClient {
  private readonly opts: ParsecClientOptions;
  private readonly store: TokenStore;
  private readonly refresh: RefreshController;
  private centrifuge: Centrifuge | null = null;
  private manifestCache: Manifest | null = null;
  private readonly handlers = new Map<ClientEvent, Set<EventHandler>>();

  constructor(opts: ParsecClientOptions) {
    if (!opts.endpoint || typeof opts.endpoint !== "string") {
      throw new ParsecError(ParsecErrorCode.InvalidArgument, "endpoint is required");
    }
    this.opts = opts;
    this.store =
      opts.tokenStore ??
      new MemoryTokenStore(
        opts.accessToken
          ? {
              access: opts.accessToken,
              accessExp: opts.accessExp ?? 0,
              refresh: opts.refreshToken ?? "",
              refreshExp: opts.refreshExp ?? 0,
            }
          : null,
      );
    const refreshOpts = {
      endpoint: opts.endpoint,
      store: this.store,
      skewSeconds: opts.refreshSkewSeconds ?? 30,
      onTerminal: (err: ParsecError) => this.emit("terminal", err),
      ...(opts.fetch ? { fetch: opts.fetch } : {}),
    };
    this.refresh = new RefreshController(refreshOpts);
  }

  /** Subscribe to a wrapper-level event (`terminal`, `manifestUnavailable`). */
  on(event: ClientEvent, handler: EventHandler): this {
    let set = this.handlers.get(event);
    if (!set) {
      set = new Set();
      this.handlers.set(event, set);
    }
    set.add(handler);
    return this;
  }

  /** Remove an event handler. */
  off(event: ClientEvent, handler: EventHandler): this {
    this.handlers.get(event)?.delete(handler);
    return this;
  }

  /** Fetch and cache the server manifest. Cached value is returned on second call. */
  async fetchManifest(opts?: FetchManifestOptions): Promise<Manifest> {
    if (this.manifestCache) return this.manifestCache;
    const merged: FetchManifestOptions = { ...opts };
    if (!merged.fetch && this.opts.fetch) merged.fetch = this.opts.fetch;
    this.manifestCache = await fetchManifest(this.opts.endpoint, merged);
    return this.manifestCache;
  }

  /** Drop the cached manifest so the next fetchManifest goes to the server. */
  invalidateManifest(): void {
    this.manifestCache = null;
  }

  /** Open the realtime connection. Idempotent: a second call is a no-op when already connected. */
  async connect(): Promise<void> {
    if (this.centrifuge) {
      this.centrifuge.connect();
      return;
    }
    const endpoints = await this.resolveEndpoints();
    const opts = await this.buildCentrifugeOptions();
    this.centrifuge = new Centrifuge(endpoints, opts);
    void this.refresh.schedulePreemptive();
    this.centrifuge.connect();
  }

  /** Disconnect the realtime channel and stop the refresh timer. */
  disconnect(): void {
    this.refresh.dispose();
    this.centrifuge?.disconnect();
  }

  /** Allocate a Subscription after validating the channel name. */
  newSubscription(channel: string, options?: SubscriptionOptions): Subscription {
    parseName(channel);
    const c = this.ensureCentrifuge();
    return c.newSubscription(channel, options);
  }

  /**
   * Allocate a phantom-typed Subscription. T is the publication data shape
   * the channel's schema produces; the runtime type is the same Subscription.
   */
  typedSubscribe<T = unknown>(
    channel: string,
    options?: SubscriptionOptions,
  ): TypedSubscription<T> {
    return this.newSubscription(channel, options) as unknown as TypedSubscription<T>;
  }

  /** Server-side publish via the realtime channel. The wrapper validates the name first. */
  async publish(channel: string, data: unknown): Promise<void> {
    parseName(channel);
    await this.ensureCentrifuge().publish(channel, data);
  }

  /** Underlying Centrifuge for advanced cases. Escape hatch — prefer wrapper methods. */
  raw(): Centrifuge {
    return this.ensureCentrifuge();
  }

  /** Return the current TokenStore. */
  tokens(): TokenStore {
    return this.store;
  }

  private ensureCentrifuge(): Centrifuge {
    if (!this.centrifuge) {
      throw new ParsecError(ParsecErrorCode.Internal, "client is not connected; call connect() first");
    }
    return this.centrifuge;
  }

  private async resolveEndpoints(): Promise<string | TransportEndpoint[]> {
    const preferred = this.opts.preferTransports ?? DEFAULT_TRANSPORTS;
    let serverTransports: TransportName[] | undefined;
    if (this.opts.manifest !== false) {
      try {
        const m = await this.fetchManifest();
        serverTransports = (m.transports as TransportName[] | undefined) ?? undefined;
      } catch (err) {
        this.emit("manifestUnavailable", err);
      }
    }
    const picked = pickTransports(serverTransports, preferred);
    return picked.map((t) => ({
      transport: t,
      endpoint: transportEndpoint(this.opts.endpoint, t),
    }));
  }

  private async buildCentrifugeOptions(): Promise<Partial<CentrifugeOptions>> {
    const pair = await Promise.resolve(this.store.get());
    const initial = pair?.access ?? "";
    const merged: Partial<CentrifugeOptions> = {
      ...(this.opts.centrifugeOptions ?? {}),
      token: initial,
      getToken: async () => this.refresh.ensureFresh(),
    };
    return merged;
  }

  private emit(event: ClientEvent, ...args: any[]): void {
    const set = this.handlers.get(event);
    if (!set) return;
    for (const handler of set) {
      try {
        handler(...args);
      } catch {
        // user handler errors are swallowed; they're not the wrapper's problem
      }
    }
  }
}
