/**
 * Token storage contract + default in-memory implementation. Custom
 * adapters (cookie, IndexedDB, secure-storage) implement TokenStore.
 */

/**
 * TokenPair holds both halves of a Parsec credential plus their unix-second
 * expiries from the most recent RefreshToken response. accessExp / refreshExp
 * are 0 when unknown — callers SHOULD treat 0 as "expired" and trigger a
 * refresh on next use.
 */
export interface TokenPair {
  access: string;
  accessExp: number;
  refresh: string;
  refreshExp: number;
}

/**
 * TokenStore is the pluggable persistence boundary. Implementations must
 * be safe for concurrent get/set/clear from the same caller (typically
 * the ParsecClient instance).
 */
export interface TokenStore {
  get(): Promise<TokenPair | null> | TokenPair | null;
  set(pair: TokenPair): Promise<void> | void;
  clear(): Promise<void> | void;
}

/**
 * MemoryTokenStore is the default. Tokens are lost on page reload — wire a
 * persistent adapter for browsers that need session continuity.
 */
export class MemoryTokenStore implements TokenStore {
  private pair: TokenPair | null;

  constructor(initial?: Partial<TokenPair> | null) {
    if (initial && initial.access && initial.refresh) {
      this.pair = {
        access: initial.access,
        accessExp: initial.accessExp ?? 0,
        refresh: initial.refresh,
        refreshExp: initial.refreshExp ?? 0,
      };
    } else {
      this.pair = null;
    }
  }

  get(): TokenPair | null {
    return this.pair;
  }

  set(pair: TokenPair): void {
    this.pair = { ...pair };
  }

  clear(): void {
    this.pair = null;
  }
}
