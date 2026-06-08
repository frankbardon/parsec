/**
 * Parity gate against channels/name_test.go. Cases here mirror the Go
 * tests case-for-case — adding a Go case requires adding the matching
 * case here.
 */
import { describe, it, expect } from "vitest";
import { buildName, formatName, isPrivate, parseName, Visibility } from "../src/channels.js";
import { ParsecError, ParsecErrorCode } from "../src/errors.js";

describe("parseName — Go parity", () => {
  it("parses public:webapp.system.status", () => {
    const n = parseName("public:webapp.system.status");
    expect(n.visibility).toBe(Visibility.Public);
    expect(n.app).toBe("webapp");
    expect(n.domain).toBe("system");
    expect(n.id).toBe("status");
    expect(n.topic).toBe("");
    expect(formatName(n)).toBe("public:webapp.system.status");
  });

  it("rejects private without id", () => {
    expect(() => parseName("private:webapp.session")).toThrow(ParsecError);
  });

  it("parses private:agent.analysis.abc123.progress", () => {
    const n = parseName("private:agent.analysis.abc123.progress");
    expect(isPrivate(n)).toBe(true);
    expect(n.topic).toBe("progress");
  });

  it("rejects unknown visibility", () => {
    expect(() => parseName("secret:webapp.x.y")).toThrow(ParsecError);
  });

  it("rejects extra colon", () => {
    expect(() => parseName("public:webapp:session.a")).toThrow(ParsecError);
  });

  it.each([
    "public:Webapp.system.x",
    "public:web app.sys.x",
    "public:webapp..x",
    "public:webapp.sys.x.y.z",
    "public:",
    "",
  ])("rejects bad input %s", (bad) => {
    expect(() => parseName(bad)).toThrow(ParsecError);
  });

  it("ParsecError carries CHANNEL_INVALID code", () => {
    try {
      parseName("");
      throw new Error("should have thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(ParsecError);
      expect((err as ParsecError).code).toBe(ParsecErrorCode.ChannelInvalid);
    }
  });
});

describe("buildName", () => {
  it("builds private:webapp.user.42.downloads", () => {
    const n = buildName(Visibility.Private, "webapp", "user", "42", "downloads");
    expect(formatName(n)).toBe("private:webapp.user.42.downloads");
  });

  it("rejects private without id at build time", () => {
    expect(() => buildName(Visibility.Private, "webapp", "user")).toThrow(ParsecError);
  });

  it("accepts hyphens and underscores in components", () => {
    const n = buildName(Visibility.Public, "my-app", "domain_one", "id-2");
    expect(formatName(n)).toBe("public:my-app.domain_one.id-2");
  });

  it("rejects uppercase", () => {
    expect(() => buildName(Visibility.Public, "App", "domain")).toThrow(ParsecError);
  });
});
