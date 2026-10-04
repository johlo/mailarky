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

Use `UID FETCH` and `UID SEARCH` as distinct IMAP commands. `CONNECT` is the
greeting hook and requires a server rule in `before` phase. Greeting and TLS
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
| `disconnect` | Optional `silent` and `message`; otherwise SMTP 421 or IMAP BYE precedes close |
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

## Bound and remove a rule

Optional `max_hits`, `probability` (0–1), and `expires_at` (RFC3339) bound a rule.
`enabled` defaults to true. Hits are claimed atomically across simultaneous
connections. Expired, disabled, and exhausted rules do not claim hits.

```sh
curl -fsS -X PATCH http://localhost:8026/api/v1/accounts/ACCOUNT_ID/faults/retry-once \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
curl -fsS -X DELETE http://localhost:8026/api/v1/accounts/ACCOUNT_ID/faults/retry-once
```

Rules disappear on restart, even with persistent mail storage. Replacing a
rule with PUT preserves its hit count; delete and recreate to reset it.

## TCP-level faults with Toxiproxy

Put [Toxiproxy](https://github.com/Shopify/toxiproxy) between the application's
SMTP/IMAP clients and Mailarky. Point each application connection at a proxy
listener whose upstream is `mailarky:1025` or `mailarky:1993` on the Compose
network. Continue calling Mailarky's HTTP API directly.

Use Toxiproxy for latency, bandwidth limits, resets, and unavailable TCP
connections. Use Mailarky for protocol replies, authentication failures,
command failures, and selective message anomalies. When proxying TLS, keep
certificate hostname verification configured for the Mailarky certificate.
