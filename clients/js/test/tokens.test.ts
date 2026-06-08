import { describe, it, expect } from "vitest";
import { MemoryTokenStore } from "../src/tokens.js";

describe("MemoryTokenStore", () => {
  it("returns null when uninitialized", () => {
    const s = new MemoryTokenStore();
    expect(s.get()).toBeNull();
  });

  it("initializes from constructor pair", () => {
    const s = new MemoryTokenStore({ access: "A", refresh: "R", accessExp: 1, refreshExp: 2 });
    expect(s.get()).toEqual({ access: "A", refresh: "R", accessExp: 1, refreshExp: 2 });
  });

  it("ignores partial initializer with missing refresh", () => {
    const s = new MemoryTokenStore({ access: "A" });
    expect(s.get()).toBeNull();
  });

  it("set persists a copy (mutating caller does not affect store)", () => {
    const s = new MemoryTokenStore();
    const pair = { access: "A", refresh: "R", accessExp: 10, refreshExp: 20 };
    s.set(pair);
    pair.access = "MUTATED";
    expect(s.get()?.access).toBe("A");
  });

  it("clear empties the store", () => {
    const s = new MemoryTokenStore({ access: "A", refresh: "R", accessExp: 1, refreshExp: 2 });
    s.clear();
    expect(s.get()).toBeNull();
  });
});
