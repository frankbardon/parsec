import { describe, it, expect, vi } from "vitest";
import { RefreshController } from "../src/refresh.js";
import { MemoryTokenStore } from "../src/tokens.js";
import { ParsecError, ParsecErrorCode } from "../src/errors.js";

function buildFetch(
  responder: (req: { url: string; body: unknown }) => Response | Promise<Response>,
): typeof fetch {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    const body = init?.body ? JSON.parse(init.body as string) : null;
    return responder({ url, body });
  }) as unknown as typeof fetch;
}

function okResp(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("RefreshController.ensureFresh", () => {
  it("returns cached access when accessExp is in the future beyond skew", async () => {
    const now = 1_000_000;
    const store = new MemoryTokenStore({
      access: "A1",
      accessExp: now / 1000 + 1000,
      refresh: "R1",
      refreshExp: now / 1000 + 5000,
    });
    const f = buildFetch(() => {
      throw new Error("should not be called");
    });
    const ctl = new RefreshController({
      endpoint: "https://parsec.test",
      store,
      now: () => now,
      fetch: f,
    });
    const tok = await ctl.ensureFresh();
    expect(tok).toBe("A1");
    expect(f).not.toHaveBeenCalled();
    ctl.dispose();
  });

  it("refreshes when accessExp is inside the skew window and persists rotation", async () => {
    const now = 1_000_000;
    const store = new MemoryTokenStore({
      access: "A1",
      accessExp: now / 1000 + 10, // inside default 30s skew
      refresh: "R1",
      refreshExp: now / 1000 + 5000,
    });
    const f = buildFetch(({ body }) => {
      expect(body).toEqual({ refreshToken: "R1" });
      return okResp({
        accessToken: "A2",
        accessExpiresUnix: now / 1000 + 600,
        refreshToken: "R2",
        refreshExpiresUnix: now / 1000 + 7000,
        rotated: true,
      });
    });
    const ctl = new RefreshController({
      endpoint: "https://parsec.test",
      store,
      now: () => now,
      fetch: f,
    });
    const tok = await ctl.ensureFresh();
    expect(tok).toBe("A2");
    const pair = store.get();
    expect(pair?.access).toBe("A2");
    expect(pair?.refresh).toBe("R2");
    expect(pair?.refreshExp).toBe(now / 1000 + 7000);
    ctl.dispose();
  });

  it("de-duplicates concurrent refreshes (single-flight)", async () => {
    const now = 1_000_000;
    const store = new MemoryTokenStore({
      access: "A1",
      accessExp: now / 1000 + 1, // forces refresh
      refresh: "R1",
      refreshExp: now / 1000 + 5000,
    });
    let calls = 0;
    const f = buildFetch(() => {
      calls++;
      return okResp({
        accessToken: "A2",
        accessExpiresUnix: now / 1000 + 600,
        refreshToken: "R2",
        refreshExpiresUnix: now / 1000 + 7000,
        rotated: true,
      });
    });
    const ctl = new RefreshController({
      endpoint: "https://parsec.test",
      store,
      now: () => now,
      fetch: f,
    });
    const [a, b, c] = await Promise.all([
      ctl.ensureFresh(),
      ctl.ensureFresh(),
      ctl.ensureFresh(),
    ]);
    expect(a).toBe("A2");
    expect(b).toBe("A2");
    expect(c).toBe("A2");
    expect(calls).toBe(1);
    ctl.dispose();
  });

  it("preserves old refresh when server returns empty refreshToken (legacy path)", async () => {
    const now = 1_000_000;
    const store = new MemoryTokenStore({
      access: "A1",
      accessExp: now / 1000 + 1,
      refresh: "R-legacy",
      refreshExp: now / 1000 + 5000,
    });
    const f = buildFetch(() =>
      okResp({
        accessToken: "A2",
        accessExpiresUnix: now / 1000 + 600,
        refreshToken: "",
        rotated: false,
      }),
    );
    const ctl = new RefreshController({
      endpoint: "https://parsec.test",
      store,
      now: () => now,
      fetch: f,
    });
    await ctl.ensureFresh();
    expect(store.get()?.refresh).toBe("R-legacy");
    ctl.dispose();
  });

  it("fires onTerminal + clears store on AuthDenied", async () => {
    const now = 1_000_000;
    const store = new MemoryTokenStore({
      access: "A1",
      accessExp: now / 1000 + 1,
      refresh: "R-bad",
      refreshExp: now / 1000 + 5000,
    });
    const f = buildFetch(
      () =>
        new Response(JSON.stringify({ code: "permission_denied", msg: "revoked" }), {
          status: 403,
          headers: { "Content-Type": "application/json" },
        }),
    );
    const onTerminal = vi.fn();
    const ctl = new RefreshController({
      endpoint: "https://parsec.test",
      store,
      now: () => now,
      fetch: f,
      onTerminal,
    });
    await expect(ctl.ensureFresh()).rejects.toBeInstanceOf(ParsecError);
    expect(onTerminal).toHaveBeenCalledOnce();
    expect(onTerminal.mock.calls[0]?.[0]?.code).toBe(ParsecErrorCode.AuthDenied);
    expect(store.get()).toBeNull();
    ctl.dispose();
  });

  it("throws AuthExpired when no refresh token is set", async () => {
    const store = new MemoryTokenStore();
    const ctl = new RefreshController({
      endpoint: "https://parsec.test",
      store,
    });
    await expect(ctl.ensureFresh()).rejects.toMatchObject({
      code: ParsecErrorCode.AuthExpired,
    });
    ctl.dispose();
  });

  it("dispose() prevents further refreshes", async () => {
    const store = new MemoryTokenStore({
      access: "A1",
      accessExp: 0,
      refresh: "R1",
      refreshExp: 0,
    });
    const ctl = new RefreshController({ endpoint: "https://parsec.test", store });
    ctl.dispose();
    await expect(ctl.ensureFresh()).rejects.toBeInstanceOf(ParsecError);
  });
});
