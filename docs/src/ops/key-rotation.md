# Key rotation runbook

This document walks the full procedure for rotating Parsec's HMAC signing
key without dropping a single live subscriber or refresh-flow client.

The contract: tokens minted under any non-retired key continue to verify.
Promoting a new key does not invalidate prior tokens. Retiring a key
does — only after you have waited for tokens signed by that key to
expire naturally.

## Preconditions

- Parsec is running with `--state-dir <dir>`. Without it the keyring is
  ephemeral and rotation does nothing useful.
- You have a valid mgmt bearer token. Export it as `PARSEC_TOKEN`.
- You know the longest TTL in use:
  - Access tokens: 5m by default
  - Refresh tokens: ≤ channel TTL (max 1h)
  - Mgmt tokens: ≤ 7d (24h default)

The wait between Promote and Retire is the **maximum of these** — for a
deployment with default TTLs that is **24 hours**.

## Procedure

```bash
export PARSEC_SERVER="https://parsec.internal:8000"
export PARSEC_TOKEN="<current mgmt bearer>"

# 1. Inspect current state. There should be exactly one active key.
parsec keys list

# 2. Generate a new key. It joins the ring as verify-only — it does not
#    sign anything yet. You can do this at any time.
NEW_KID=$(parsec keys generate | jq -r '.payload.id')
echo "new key: $NEW_KID"

# 3. Promote the new key. From this moment on every newly-minted token
#    embeds the new kid. Tokens already in flight continue to verify
#    because the previous active key is now verify-only.
parsec keys promote "$NEW_KID"

# 4. Mint a fresh mgmt bearer signed by the NEW key. Use this bearer
#    going forward — your existing bearer is still valid but will die
#    when you retire the old key in step 6.
NEW_BEARER=$(parsec tokens mgmt --ttl 24h | jq -r '.payload.mgmt_token')
export PARSEC_TOKEN="$NEW_BEARER"

# 5. Wait. The wait must cover the longest TTL of tokens that may have
#    been signed by the previous active key. With Parsec defaults this
#    is 24 hours; lower TTL deployments may need less.
sleep 86400  # not literally — schedule this, monitor in the meantime

# 6. Retire the old key. From this moment on tokens claiming the old
#    kid fail with PARSEC_AUTH_DENIED.
OLD_KID=$(parsec keys list | jq -r '.payload.keys[] | select(.role=="verify-only") | .id')
parsec keys retire "$OLD_KID"

# 7. Confirm the ring is clean.
parsec keys list
```

## SIGHUP / manual reload

If you edit `keyring.json` out-of-band (do not do this normally — go
through the CLI), the running server will pick it up either on the next
mtime-poll tick (every 5s by default) or when you signal it:

```bash
kill -HUP $(pgrep parsec)
# OR
parsec keys reload
```

Both achieve the same swap.

## Multi-node deployments

Point every Parsec node at a shared Redis (`Options.RedisClient` or
the YAML `redis.addr` field). The Redis-backed `KeyRingStore`
publishes a version event on every rotation, and every node subscribes
— `parsec keys generate/promote/retire` on any one node propagates to
the rest of the cluster within milliseconds without operator
intervention. The file-backed keyring + NFS pattern still works for
deployments without Redis; in that case disable the mtime poller on
all but one node (`--keyring-poll 0`) and `SIGHUP` the others after
each rotation.

## What if I have to break glass?

If a key has been compromised and you cannot wait for tokens to expire:

```bash
# 1. Generate + promote a new key.
NEW_KID=$(parsec keys generate | jq -r '.payload.id')
parsec keys promote "$NEW_KID"
NEW_BEARER=$(parsec tokens mgmt | jq -r '.payload.mgmt_token')
export PARSEC_TOKEN="$NEW_BEARER"

# 2. Retire the compromised key immediately. Every token signed by it
#    fails verification; live subscribers using a compromised access
#    token are kicked at next protocol heartbeat.
parsec keys retire <compromised-kid>
```

This is destructive: every in-flight token signed by that key dies.
Browser clients reconnect via the refresh flow only if their refresh
token was signed by a DIFFERENT key — otherwise the user is logged out
and must re-authenticate through your application's normal flow.

## What never works

- **You cannot retire the active key.** Promote another key first.
- **You cannot resurrect a retired key.** The disk snapshot drops
  retired entries; the in-memory ring drops them on next reload. Mint a
  new one if you need one.
- **You cannot use a key the ring does not know about.** Tokens with a
  `kid` claim that doesn't resolve to a live key fail as if the
  signature were bad. Parsec doesn't tell the attacker which case.
