import { describe, it, expect } from "vitest";
import {
  ParsecError,
  ParsecErrorCode,
  isRecoverable,
  mapTwirpError,
} from "../src/errors.js";

describe("mapTwirpError", () => {
  const cases: Array<[string, string]> = [
    ["invalid_argument", ParsecErrorCode.InvalidArgument],
    ["not_found", ParsecErrorCode.ChannelNotFound],
    ["already_exists", ParsecErrorCode.ChannelExists],
    ["failed_precondition", ParsecErrorCode.ChannelClosed],
    ["permission_denied", ParsecErrorCode.AuthDenied],
    ["unauthenticated", ParsecErrorCode.AuthExpired],
    ["unavailable", ParsecErrorCode.BrokerNotReady],
    ["resource_exhausted", ParsecErrorCode.RateLimited],
  ];

  it.each(cases)("maps Twirp %s to %s", (twirpCode, parsecCode) => {
    const err = mapTwirpError({ code: twirpCode, msg: "boom" });
    expect(err).toBeInstanceOf(ParsecError);
    expect(err.code).toBe(parsecCode);
    expect(err.message).toBe("boom");
  });

  it("falls back to INTERNAL on unmapped Twirp code", () => {
    const err = mapTwirpError({ code: "weird", msg: "nope" });
    expect(err.code).toBe(ParsecErrorCode.Internal);
    expect(err.cause).toEqual({ code: "weird", msg: "nope" });
  });

  it("uses code as message when msg is missing", () => {
    const err = mapTwirpError({ code: "invalid_argument" } as never);
    expect(err.message).toBe("invalid_argument");
  });
});

describe("isRecoverable", () => {
  it("treats network/server hiccups as recoverable", () => {
    expect(isRecoverable(new ParsecError(ParsecErrorCode.RateLimited, "x"))).toBe(true);
    expect(isRecoverable(new ParsecError(ParsecErrorCode.BrokerNotReady, "x"))).toBe(true);
    expect(isRecoverable(new ParsecError(ParsecErrorCode.Internal, "x"))).toBe(true);
  });

  it("treats auth + validation failures as terminal", () => {
    expect(isRecoverable(new ParsecError(ParsecErrorCode.AuthDenied, "x"))).toBe(false);
    expect(isRecoverable(new ParsecError(ParsecErrorCode.AuthExpired, "x"))).toBe(false);
    expect(isRecoverable(new ParsecError(ParsecErrorCode.ChannelInvalid, "x"))).toBe(false);
  });

  it("treats non-ParsecError as recoverable (network blip etc.)", () => {
    expect(isRecoverable(new Error("network"))).toBe(true);
  });
});
