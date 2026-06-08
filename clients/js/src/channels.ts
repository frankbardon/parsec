/**
 * Channel-name grammar — port of `channels/name.go`. MUST stay in sync.
 * Any change to the Go grammar requires the same change here in the same
 * PR plus a mirrored test in `test/channels.test.ts`.
 *
 * Grammar:
 *
 *   <visibility>:<app>.<domain>[.<id>][.<topic>]
 *
 * - visibility is exactly "public" or "private"
 * - components are lowercase ASCII letters, digits, hyphen, underscore
 * - private channels MUST include an id segment
 * - the ":" character is reserved for the visibility prefix
 */
import { ParsecError, ParsecErrorCode } from "./errors.js";

export const Visibility = {
  Public: "public",
  Private: "private",
} as const;

export type Visibility = (typeof Visibility)[keyof typeof Visibility];

export interface Name {
  visibility: Visibility;
  app: string;
  domain: string;
  /** Optional for public, required for private. */
  id: string;
  /** Optional. */
  topic: string;
}

const COMPONENT_RE = /^[a-z0-9_-]+$/;

function validateComponent(component: string): void {
  if (component === "") {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      "channel component must not be empty",
    );
  }
  if (!COMPONENT_RE.test(component)) {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      `invalid character in channel component ${JSON.stringify(component)}`,
    );
  }
}

/**
 * Parse and validate a wire-form channel name. Throws ParsecError(PARSEC_CHANNEL_INVALID)
 * on any rule violation.
 */
export function parseName(s: string): Name {
  const colon = s.indexOf(":");
  if (colon <= 0 || colon === s.length - 1) {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      "channel name must start with public: or private:",
    );
  }
  const vis = s.slice(0, colon);
  if (vis !== Visibility.Public && vis !== Visibility.Private) {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      `unknown visibility ${JSON.stringify(vis)}; want public or private`,
    );
  }
  const rest = s.slice(colon + 1);
  if (rest.includes(":")) {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      "the : character is reserved for the visibility prefix",
    );
  }
  const parts = rest.split(".");
  if (parts.length < 2) {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      "channel name must include at least <app>.<domain>",
    );
  }
  if (parts.length > 4) {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      "channel name has too many components; max is <app>.<domain>.<id>.<topic>",
    );
  }
  for (const p of parts) {
    validateComponent(p);
  }
  const name: Name = {
    visibility: vis,
    app: parts[0] as string,
    domain: parts[1] as string,
    id: parts.length >= 3 ? (parts[2] as string) : "",
    topic: parts.length === 4 ? (parts[3] as string) : "",
  };
  if (name.visibility === Visibility.Private && name.id === "") {
    throw new ParsecError(
      ParsecErrorCode.ChannelInvalid,
      "private channels must include an id segment",
    );
  }
  return name;
}

/**
 * Render a Name to its canonical wire form. Round-trips through parseName.
 */
export function formatName(name: Name): string {
  const parts: string[] = [name.app, name.domain];
  if (name.id !== "") parts.push(name.id);
  if (name.topic !== "") parts.push(name.topic);
  return `${name.visibility}:${parts.join(".")}`;
}

/**
 * Compose and validate a Name from its components. Same rules as parseName.
 */
export function buildName(
  visibility: Visibility,
  app: string,
  domain: string,
  id = "",
  topic = "",
): Name {
  const candidate: Name = { visibility, app, domain, id, topic };
  parseName(formatName(candidate));
  return candidate;
}

/**
 * isPrivate reports whether the parsed channel is in the private namespace.
 */
export function isPrivate(name: Name): boolean {
  return name.visibility === Visibility.Private;
}
