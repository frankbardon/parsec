import { describe, it, expect, vi } from "vitest";
import {
  fetchManifest,
  pickTransports,
  transportEndpoint,
} from "../src/manifest.js";
import { ParsecError, ParsecErrorCode } from "../src/errors.js";
import type { TransportName } from "../src/types.js";

function mockFetch(body: unknown, init: ResponseInit = { status: 200 }): typeof fetch {
  return vi.fn(async (input: RequestInfo | URL, _opts?: RequestInit) => {
    void input;
    return new Response(JSON.stringify(body), {
      ...init,
      headers: { "Content-Type": "application/json", ...(init.headers ?? {}) },
    });
  }) as unknown as typeof fetch;
}

describe("fetchManifest", () => {
  it("unwraps the descriptor envelope and returns the payload", async () => {
    const payload = {
      service: "parsec",
      version: "0.42.0",
      transports: ["websocket", "http_stream"],
    };
    const envelope = {
      format_version: "1.0",
      kind: "Manifest",
      generated_at: "2026-06-08T00:00:00Z",
      payload,
    };
    const f = mockFetch(envelope);
    const m = await fetchManifest("https://parsec.test", { fetch: f });
    expect(m.transports).toEqual(["websocket", "http_stream"]);
    expect(m.version).toBe("0.42.0");
    expect(f).toHaveBeenCalledWith(
      "https://parsec.test/manifest",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("throws BROKER_NOT_READY on non-2xx", async () => {
    const f = mockFetch({}, { status: 503, statusText: "down" });
    await expect(fetchManifest("https://parsec.test", { fetch: f })).rejects.toMatchObject({
      code: ParsecErrorCode.BrokerNotReady,
    });
  });

  it("throws BROKER_NOT_READY on network failure", async () => {
    const f = vi.fn(async () => {
      throw new TypeError("network");
    }) as unknown as typeof fetch;
    await expect(fetchManifest("https://parsec.test", { fetch: f })).rejects.toBeInstanceOf(
      ParsecError,
    );
  });

  it("throws INTERNAL on missing envelope", async () => {
    const f = mockFetch({ not: "an envelope" });
    await expect(fetchManifest("https://parsec.test", { fetch: f })).rejects.toMatchObject({
      code: ParsecErrorCode.Internal,
    });
  });
});

describe("pickTransports", () => {
  it("intersects + preserves client preference order", () => {
    const got = pickTransports(["http_stream", "websocket"], ["websocket", "http_stream"]);
    expect(got).toEqual(["websocket", "http_stream"]);
  });

  it("filters out transports the server does not advertise", () => {
    const got = pickTransports(["websocket"], ["webtransport", "websocket", "http_stream"]);
    expect(got).toEqual(["websocket"]);
  });

  it("degrades to websocket when server advertises an empty list", () => {
    expect(pickTransports([], ["http_stream"])).toEqual(["http_stream"]);
    expect(pickTransports(undefined, ["websocket", "http_stream"])).toEqual(["websocket"]);
  });

  it("returns ['websocket'] when there is zero overlap", () => {
    const got = pickTransports(["http_stream"], ["webtransport"]);
    expect(got).toEqual(["websocket"]);
  });
});

describe("transportEndpoint", () => {
  it("upgrades websocket scheme on https", () => {
    expect(transportEndpoint("https://parsec.test", "websocket")).toBe(
      "wss://parsec.test/connection/websocket",
    );
  });

  it("upgrades websocket scheme on http", () => {
    expect(transportEndpoint("http://localhost:8000", "websocket")).toBe(
      "ws://localhost:8000/connection/websocket",
    );
  });

  it.each<[TransportName, string]>([
    ["http_stream", "https://parsec.test/connection/http_stream"],
    ["webtransport", "https://parsec.test/connection/webtransport"],
    ["sse", "https://parsec.test/connection/sse"],
  ])("keeps %s on HTTP scheme", (transport, want) => {
    expect(transportEndpoint("https://parsec.test", transport)).toBe(want);
  });

  it("trims trailing slash on baseUrl", () => {
    expect(transportEndpoint("https://parsec.test/", "websocket")).toBe(
      "wss://parsec.test/connection/websocket",
    );
  });
});
