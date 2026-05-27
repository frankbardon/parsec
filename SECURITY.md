# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it responsibly.

**Do not open a public issue.** Instead, email security concerns to the maintainer or use [GitHub's private vulnerability reporting](https://github.com/frankbardon/parsec/security/advisories/new).

Please include:

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You should receive a response within 72 hours. We'll work with you to understand the issue and coordinate a fix before any public disclosure.

## Scope

Security issues in the following areas are in scope:

- **Channel authorization** — public vs private channel access control, token issuance, namespace gates
- **Broker** — wire protocol handling, message validation, presence and history correctness
- **CLI** — command parsing, credential handling, server URL validation
- **Twirp surface** — RPC handler input validation, error mapping
- **Sinks** — outbound credentials (email, slack, webhook) handling and redaction
- **Persistence** — channel metadata storage, TTL enforcement

## Known Considerations

- **Private channel secrets**: Issued once at channel creation, never persisted in plaintext. The server stores a hash; tokens are minted from the secret.
- **TTL enforcement**: Private channels have a hard cap of one hour. The TTL is enforced by the broker tick loop, not by client trust.
- **CLI scoping**: The CLI honors the same channel-level ACL as a regular client. It cannot bypass server-side authorization.
