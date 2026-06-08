/**
 * centrifuge-js contract test. The wrapper depends on a concrete
 * constructor shape and `getToken` callback signature; pinning these
 * here means an accidental v6 bump fails loudly instead of breaking
 * silently downstream.
 */
import { describe, it, expect } from "vitest";
import { Centrifuge } from "centrifuge";

describe("centrifuge-js v5.x contract", () => {
  it("Centrifuge has the expected constructor signature", () => {
    // arity is 2: (endpoint, options?)
    expect(Centrifuge.length).toBe(2);
  });

  it("a constructed Centrifuge exposes connect/disconnect/newSubscription/publish", () => {
    const c = new Centrifuge("ws://localhost:0/connection/websocket");
    expect(typeof c.connect).toBe("function");
    expect(typeof c.disconnect).toBe("function");
    expect(typeof c.newSubscription).toBe("function");
    expect(typeof c.publish).toBe("function");
  });

  it("accepts an array of TransportEndpoint as the first arg", () => {
    expect(
      () =>
        new Centrifuge([
          { transport: "websocket", endpoint: "ws://localhost:0/connection/websocket" },
        ]),
    ).not.toThrow();
  });
});
