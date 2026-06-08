import { describe, it, expect } from "vitest";
import { decodeJwtPayload, inspectScopes } from "../src/scopes.js";
import { ParsecError, ParsecErrorCode } from "../src/errors.js";
import { ParsecVerb } from "../src/types.js";

// Build a JWT-shaped string from a payload object. Signature segment is
// arbitrary — the decoder never looks at it.
function makeJwt(payload: object, sig = "deadbeef"): string {
  const header = b64url(JSON.stringify({ alg: "HS256", kid: "k1", typ: "JWT" }));
  const body = b64url(JSON.stringify(payload));
  return `${header}.${body}.${sig}`;
}

function b64url(s: string): string {
  const b64 =
    typeof btoa === "function" ? btoa(s) : Buffer.from(s, "utf-8").toString("base64");
  return b64.replace(/=+$/, "").replace(/\+/g, "-").replace(/\//g, "_");
}

describe("decodeJwtPayload", () => {
  it("decodes a well-formed access token payload", () => {
    const token = makeJwt({
      sub: "alice",
      typ: "access",
      chs: ["public:webapp.lobby.global"],
      iat: 1_700_000_000,
      exp: 1_700_000_300,
    });
    const claims = decodeJwtPayload(token);
    expect(claims.sub).toBe("alice");
    expect(claims.typ).toBe("access");
    expect(claims.chs).toEqual(["public:webapp.lobby.global"]);
    expect(claims.iat).toBe(1_700_000_000);
    expect(claims.exp).toBe(1_700_000_300);
  });

  it("throws PARSEC_INVALID_ARGUMENT for empty input", () => {
    expect(() => decodeJwtPayload("")).toThrowError(
      expect.objectContaining({ code: ParsecErrorCode.InvalidArgument }),
    );
  });

  it("throws PARSEC_INVALID_ARGUMENT for non-JWT shape", () => {
    expect(() => decodeJwtPayload("not.a.jwt.really")).toThrow(ParsecError);
    expect(() => decodeJwtPayload("only.two")).toThrow(ParsecError);
  });

  it("throws on invalid base64url", () => {
    expect(() => decodeJwtPayload("aa.@@@.bb")).toThrow(ParsecError);
  });
});

describe("inspectScopes", () => {
  it("renders chs + scopes + flags", () => {
    const token = makeJwt({
      sub: "bob",
      typ: "access",
      chs: ["private:webapp.user.42.inbox"],
      iat: 1_700_000_000,
      exp: 1_700_000_300,
      scopes: [
        { pat: "private:webapp.user.42.*", v: ["subscribe", "publish"] },
        { pat: "private:webapp.user.42.admin", v: ["manage"], deny: true },
      ],
      jti: "j-1",
      fid: "f-1",
    });
    const view = inspectScopes(token, 1_700_000_100 * 1000);
    expect(view.subject).toBe("bob");
    expect(view.channels).toEqual(["private:webapp.user.42.inbox"]);
    expect(view.scopes).toHaveLength(2);
    expect(view.scopes[0]).toEqual({
      pattern: "private:webapp.user.42.*",
      verbs: [ParsecVerb.Subscribe, ParsecVerb.Publish],
      deny: false,
    });
    expect(view.scopes[1]?.deny).toBe(true);
    expect(view.expired).toBe(false);
    expect(view.jti).toBe("j-1");
    expect(view.fid).toBe("f-1");
    expect(view.expiresAt?.getTime()).toBe(1_700_000_300 * 1000);
  });

  it("marks expired token as such", () => {
    const token = makeJwt({ typ: "access", exp: 1_000 });
    const view = inspectScopes(token, 2_000 * 1000);
    expect(view.expired).toBe(true);
  });

  it("handles tokens with no chs/scopes", () => {
    const token = makeJwt({ typ: "access" });
    const view = inspectScopes(token);
    expect(view.channels).toEqual([]);
    expect(view.scopes).toEqual([]);
  });
});
