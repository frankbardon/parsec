/**
 * JWT payload inspector — DECODE ONLY. Never verifies the signature, never
 * trusts the result for authorization decisions. The server is the only
 * authority on token validity; this module exists so UIs can display the
 * channels/scopes a token grants without round-tripping the server.
 */
import { ParsecError, ParsecErrorCode } from "./errors.js";
import type { Claims, Scope, ParsecVerb } from "./types.js";

/**
 * decodeJwtPayload splits token on "." and base64url-decodes the middle
 * segment as JSON. Does NOT verify the signature. Throws
 * PARSEC_INVALID_ARGUMENT for malformed input.
 */
export function decodeJwtPayload(token: string): Claims {
  if (typeof token !== "string" || token === "") {
    throw new ParsecError(ParsecErrorCode.InvalidArgument, "empty token");
  }
  const parts = token.split(".");
  if (parts.length !== 3) {
    throw new ParsecError(ParsecErrorCode.InvalidArgument, "malformed JWT");
  }
  const middle = parts[1] as string;
  let json: string;
  try {
    json = base64UrlDecode(middle);
  } catch (cause) {
    throw new ParsecError(ParsecErrorCode.InvalidArgument, "JWT payload is not valid base64url", { cause });
  }
  try {
    return JSON.parse(json) as Claims;
  } catch (cause) {
    throw new ParsecError(ParsecErrorCode.InvalidArgument, "JWT payload is not valid JSON", { cause });
  }
}

/**
 * inspectScopes decodes the payload and returns a structured view a UI
 * can render. The `expired` flag is a wall-clock comparison against the
 * caller's `now` (or Date.now() by default); it is NOT a signature check.
 */
export interface ScopeInspection {
  subject?: string;
  type?: Claims["typ"];
  channels: string[];
  scopes: Array<{
    pattern: string;
    verbs: ParsecVerb[];
    deny: boolean;
  }>;
  issuedAt?: Date;
  expiresAt?: Date;
  expired: boolean;
  jti?: string;
  fid?: string;
  raw: Claims;
}

export function inspectScopes(token: string, now: number = Date.now()): ScopeInspection {
  const claims = decodeJwtPayload(token);
  const channels = Array.isArray(claims.chs) ? [...claims.chs] : [];
  const scopes = Array.isArray(claims.scopes)
    ? claims.scopes.map((s: Scope) => ({
        pattern: s.pat,
        verbs: Array.isArray(s.v) ? [...s.v] : [],
        deny: s.deny === true,
      }))
    : [];
  const exp = typeof claims.exp === "number" ? claims.exp : undefined;
  const iat = typeof claims.iat === "number" ? claims.iat : undefined;
  const expired = exp !== undefined && exp * 1000 <= now;
  const result: ScopeInspection = {
    channels,
    scopes,
    expired,
    raw: claims,
  };
  if (claims.sub !== undefined) result.subject = claims.sub;
  if (claims.typ !== undefined) result.type = claims.typ;
  if (iat !== undefined) result.issuedAt = new Date(iat * 1000);
  if (exp !== undefined) result.expiresAt = new Date(exp * 1000);
  if (claims.jti !== undefined) result.jti = claims.jti;
  if (claims.fid !== undefined) result.fid = claims.fid;
  return result;
}

function base64UrlDecode(s: string): string {
  // base64url → base64
  let b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  const pad = b64.length % 4;
  if (pad === 2) b64 += "==";
  else if (pad === 3) b64 += "=";
  else if (pad !== 0) throw new Error("invalid base64url length");

  // Decode. Prefer atob in browser/jsdom; fall back to Buffer in node-only.
  let bytes: Uint8Array;
  if (typeof atob === "function") {
    const bin = atob(b64);
    bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  } else {
    const { Buffer } = require("node:buffer") as typeof import("node:buffer");
    bytes = Buffer.from(b64, "base64");
  }
  return new TextDecoder("utf-8").decode(bytes);
}
