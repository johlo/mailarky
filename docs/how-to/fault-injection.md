# Inject SMTP and IMAP failures

Create rules through HTTP, run the application, inspect rule hits, and remove
or disable the rules to test recovery. Use account rules for parallel tests.
Use server rules when the test owns the whole Mailarky instance.

## Choose the registry and trigger

POST account rules to `/api/v1/accounts/ACCOUNT_ID/faults`. POST server rules
to `/api/v1/faults`. Server rules are applied before account rules. Each registry
keeps rules in creation order; an action that terminates handling stops later
actions. Hits count claims, including rules claimed alongside a terminating action.

Every rule requires `trigger.protocol` (`smtp` or `imap`), an uppercase
`trigger.command`, and `trigger.phase`:

| Phase | Hook |
| --- | --- |
| `before` | Before command handling; AUTH/LOGIN account rules wait for the username, MAIL/RCPT rules wait for validated envelope arguments |
| `content` | SMTP DATA/BDAT after the complete MIME body arrives and before storing; IMAP SEARCH/FETCH once per matching candidate message |
| `after` | Successful command completion, before the final reply; SMTP DATA after storing; BDAT after each successful chunk (message filters are available after LAST) |
| `active` | IMAP IDLE after the continuation; schedule a disconnect relative to the start of this IDLE |
| `notification` | IMAP IDLE once per outgoing EXISTS, EXPUNGE, or flags notification |

Use `UID FETCH` and `UID SEARCH` as distinct IMAP commands. `CONNECT` is the
greeting event and requires a server rule in `before` phase. With implicit
TLS, the handshake finishes before greeting delays or disconnects take effect.
A client-sent `CONNECT` line does not trigger greeting rules. Greeting and TLS
faults cannot target an account, since authentication has not identified one.
Authentication, TLS negotiation, and logout faults require `before` phase.

SMTP account MAIL rules require AUTH to identify the account at MAIL. Anonymous
SMTP chooses accounts at RCPT; a MAIL rule is never deferred to RCPT. For an
anonymous sender failure, use a server MAIL rule with an exact sender filter.

## Reject one submission, then recover

```sh
curl -fsS http://localhost:8026/api/v1/accounts/ACCOUNT_ID/faults \
  -H 'Content-Type: application/json' -d '{
    "name":"retry-once",
    "trigger":{"protocol":"smtp","command":"DATA","phase":"content"},
    "filter":{"message":{"headers":{"X-Test-ID":"retry-case"}}},
    "action":{"type":"reject","code":451,"enhanced_code":"4.3.0","message":"Temporary storage failure"},
    "max_hits":1
  }'
```

Send a message with `X-Test-ID: retry-case`. The first submission fails without
storing a copy; a retry succeeds. GET the rule to assert `hits: 1`.

## Fail the third match, then recover

Replace `max_hits` in the example above with:

```json
"sequence": {"steps": ["pass", "pass", "apply"]}
```

The first two matching submissions skip this rule, the third applies its
rejection, and all subsequent matches skip it. Add `"repeat":true` inside
`sequence` to repeat the three-step pattern. Steps can describe any combination
of `pass` and `apply`, up to 1,024 steps. A pass only skips this rule; other
rules and normal protocol validation still apply.

Sequences advance only when the trigger and all filters match. `matches`
counts those events, and `hits` counts claimed actions. An IMAP content rule
advances per candidate message; a RCPT rule advances per matching recipient,
not per transaction. Concurrent connections share an atomic sequence within
that rule, in the order they claim it; thread scheduling determines which
connection reaches each step. Use separate accounts for independently repeatable
tests. A sequence cannot be combined with `probability`.

## Match a command or a message

`filter` may contain `username` (server rules only), `folder` (IMAP commands
that name or select a folder), and `message`. Message fields are conjunctive,
exact selectors: `message_id`, `from`, `to`, and `headers`. Envelope addresses
are case-insensitive; header values are case-sensitive. Wildcards are rejected.
A message filter must contain at least one field.

MAIL can inspect the sender. RCPT can inspect the sender and **only the current
recipient**. MIME header selectors require SMTP `content` or `after` phases.
IMAP message filters require `content` phase. A folder or account alone is
sufficient for command faults, including commands on an empty mailbox.

```json
{
  "name": "empty-inbox-unavailable",
  "trigger": {"protocol": "imap", "command": "SELECT", "phase": "before"},
  "filter": {"folder": "INBOX"},
  "action": {"type": "reject", "status": "NO", "response_code": "UNAVAILABLE"}
}
```

## Choose an action

| `action.type` | Fields and behavior |
| --- | --- |
| `reject` | SMTP `code` 400–599, optional `enhanced_code`, `message`; IMAP `status` NO/BAD, optional `response_code`, `message` |
| `delay` | `delay_ms` 0–30000; waits without holding store or registry locks |
| `disconnect` | Optional `silent` and `message`; otherwise SMTP 421 or IMAP BYE precedes close. IDLE `active` also accepts `after_ms` (0–86400000, omitted means immediate) |
| `drop` | Suppress an IMAP IDLE notification; requires `notification` phase |
| `response` | Required raw `response`, optional `close`; `{tag}` expands to the current IMAP command tag |
| `capabilities` | Required `omit` token array; SMTP EHLO or IMAP CAPABILITY, `after` phase |
| `reset_uidvalidity` | Account IMAP SELECT/EXAMINE `before`; exact `folder` and `max_hits: 1` required |
| `hide` | Omit a selected IMAP SEARCH/FETCH candidate |
| `replace_header` | `header` and `value`; change the selected IMAP response only |
| `replace_body` | `value` containing replacement raw message bytes for the selected IMAP response |

The last three require IMAP `content` phase and a message filter. Each action
has a strict schema: unrelated fields such as `delay_ms` on a rejection are
errors. Greeting SMTP rejections accept 421 or 554; use `disconnect` for an IMAP
BYE greeting. Capability changes affect advertisements, not implementation.
IMAP capability rules can also affect the initial greeting advertisement.

A `response` action writes the supplied bytes and takes over the reply. When
simulating a malformed multiline reply, set `close: true` if the client should
not wait for more bytes. Use an after DATA disconnect to simulate a stored
message whose acknowledgement was lost. Retries can then create duplicates,
as they can with a real provider.

## Interrupt active IDLE or lose a notification

Install this account rule before the application starts IDLE:

```json
{
  "name": "idle-timeout",
  "trigger": {"protocol": "imap", "command": "IDLE", "phase": "active"},
  "filter": {"folder": "INBOX"},
  "action": {"type": "disconnect", "after_ms": 2000, "message": "IDLE timeout"},
  "max_hits": 1
}
```

The server sends BYE and closes two seconds after entering IDLE. Add
`"silent":true` to close without BYE. DONE cancels the timer; a renewed IDLE
starts a fresh interval. Active rules are claimed once on entry, so the hit is
consumed even if DONE cancels it. Replacing, disabling, or deleting a rule does
not cancel an already scheduled action. If several active disconnects match,
the earliest interval wins. Timers never block other accounts or notifications.

To exercise a missed mailbox update, install:

```json
{
  "name": "miss-next-arrival",
  "trigger": {"protocol": "imap", "command": "IDLE", "phase": "notification"},
  "filter": {"folder": "INBOX", "notification": "EXISTS"},
  "action": {"type": "drop"},
  "max_hits": 1
}
```

Seed a message while the account is idling. The message is stored, but the
next EXISTS notification is omitted. `notification` can also select `FLAGS`
or `EXPUNGE`; omit it to match all three. Sequences can select, for example,
only the third flags notification. Notifications may coalesce several store
changes, so steps count wire notifications rather than HTTP requests.

This fault intentionally advances the server's view without updating the
client, and dropped notifications are not replayed on DONE or NOOP. SELECT
again, or reconnect and resynchronize, to restore the client view. In
particular, dropping EXPUNGE can make a client's sequence numbers stale.
The rule affects IDLE updates only, not normal command responses or HTTP events.

These short intervals let a test exercise timeout and renewal behavior without
waiting for a provider's normal timeout. [RFC 2177](https://www.rfc-editor.org/rfc/rfc2177.txt)
allows an IDLE inactivity timeout and recommends renewing IDLE at least every
29 minutes. Mailarky's injected intervals deliberately accelerate that scenario.

## Exercise enforced limits

Create an account with real limits to test batching and concurrency:

```sh
curl -fsS http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{
    "limits": {
      "smtp_connections": 2,
      "imap_connections": 2,
      "send_messages": 3,
      "send_window": "1s",
      "quota_messages": 10
    }
  }'
```

Authenticate as that account. A third simultaneous connection fails at AUTH
or LOGIN; closing one frees a slot. A fourth successful SMTP submission in the
same one-second window is rejected until the window resets. Filling the store
causes SMTP, IMAP APPEND/COPY, and HTTP seeding to reject more mail; deleting
messages frees quota. These errors require no fault rule. See
[configuration](../reference/configuration.md#enforced-limits) for response
codes, byte quotas, and the instance-wide SMTP recipient limit.

## Bound and remove a rule

Optional `max_hits`, `probability` (0–1), and `expires_at` (RFC3339) bound a rule.
`enabled` defaults to true. Hits are claimed atomically across simultaneous
connections. Expired, disabled, and max-hit-exhausted rules neither advance
`matches` nor claim hits. `max_hits` counts applied steps, not passed steps.

```sh
curl -fsS -X PATCH http://localhost:8026/api/v1/accounts/ACCOUNT_ID/faults/retry-once \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
curl -fsS -X DELETE http://localhost:8026/api/v1/accounts/ACCOUNT_ID/faults/retry-once
```

Rules disappear on restart, even with persistent mail storage. Replacing a
rule with PUT preserves both counters and the sequence position; toggling also
preserves them. Delete and recreate to reset the rule. Client-supplied counters
are ignored.

## TCP-level faults with Toxiproxy

Put [Toxiproxy](https://github.com/Shopify/toxiproxy) between the application's
SMTP/IMAP clients and Mailarky. Point each application connection at a proxy
listener whose upstream is `mailarky:1025` or `mailarky:1993` on the Compose
network. Continue calling Mailarky's HTTP API directly.

Use Toxiproxy for latency, bandwidth limits, resets, and unavailable TCP
connections. Use Mailarky for protocol replies, authentication failures,
command failures, and selective message anomalies. When proxying TLS, keep
certificate hostname verification configured for the Mailarky certificate.
