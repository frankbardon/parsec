import { describe, it, expect, vi } from "vitest";
import { twirpCall } from "../src/twirp.js";
import { ParsecError, ParsecErrorCode } from "../src/errors.js";

function buildFetch(handler: (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>): typeof fetch {
  return vi.fn(handler) as unknown as typeof fetch;
}

describe("twirpCall", () => {
  it("POSTs JSON to /twirp/parsec.ParsecService/<method>", async () => {
    const f = buildFetch(async (input, init) => {
      expect(input).toBe("https://parsec.test/twirp/parsec.ParsecService/Hello");
      expect(init?.method).toBe("POST");
      const headers = init?.headers as Record<string, string>;
      expect(headers["Content-Type"]).toBe("application/json");
      expect(init?.body).toBe(JSON.stringify({ x: 1 }));
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    });
    const resp = await twirpCall<{ x: number }, { ok: boolean }>(
      "https://parsec.test",
      "Hello",
      { x: 1 },
      { fetch: f },
    );
    expect(resp.ok).toBe(true);
  });

  it("attaches Authorization header when bearer present", async () => {
    let seen = "";
    const f = buildFetch(async (_, init) => {
      seen = (init?.headers as Record<string, string>)["Authorization"]!;
      return new Response("{}", { status: 200 });
    });
    await twirpCall("https://parsec.test", "M", {}, { fetch: f, bearer: "mytok" });
    expect(seen).toBe("Bearer mytok");
  });

  it("maps a Twirp error body to a ParsecError", async () => {
    const f = buildFetch(
      async () =>
        new Response(JSON.stringify({ code: "unauthenticated", msg: "expired" }), {
          status: 401,
          headers: { "Content-Type": "application/json" },
        }),
    );
    await expect(twirpCall("https://parsec.test", "M", {}, { fetch: f })).rejects.toMatchObject({
      code: ParsecErrorCode.AuthExpired,
    });
  });

  it("falls back on HTTP status when error body is not JSON", async () => {
    const f = buildFetch(async () => new Response("nope", { status: 429 }));
    await expect(twirpCall("https://parsec.test", "M", {}, { fetch: f })).rejects.toMatchObject({
      code: ParsecErrorCode.RateLimited,
    });
  });

  it("throws BROKER_NOT_READY on network failure", async () => {
    const f = buildFetch(async () => {
      throw new TypeError("network");
    });
    await expect(twirpCall("https://parsec.test", "M", {}, { fetch: f })).rejects.toBeInstanceOf(
      ParsecError,
    );
  });
});
