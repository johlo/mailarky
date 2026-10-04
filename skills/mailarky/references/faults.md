# Mailarky fault rules

Read this before writing a fault rule other than the basic SMTP rejection in
SKILL.md. Rules are validated strictly; a rejected rule returns HTTP 400 with
the reason.

## Rule format

```json
{
  "name": "unique-name",
  "trigger": {"protocol": "smtp", "command": "DATA", "phase": "content"},
  "filter": {"message": {"headers": {"X-Test-ID": "case-7"}}},
  "action": {"type": "reject", "code": 451},
  "max_hits": 1
}
```

- **Registry:** `POST /api/v1/accounts/{id}/faults` affects one account, which
  is what parallel tests need. `POST /api/v1/faults` affects the whole server;
  use it only for events that happen before login (`CONNECT`, `STARTTLS`) or
  when the test owns the server.
- **Manage rules:** `GET` the collection or `/{name}`, `PUT /{name}` to create
  or replace, `PATCH /{name}` with `{"enabled": false}`, `DELETE /{name}`.
  Rules are lost when Mailarky restarts. Replacing a rule keeps its counters;
  delete and recreate it to reset them.
- **Counters:** `matches` counts events that matched the trigger and filter;
  `hits` counts times the action applied.

## Triggers

`protocol` is `smtp` or `imap`. `command` is uppercase.

- SMTP: `CONNECT EHLO HELO AUTH STARTTLS MAIL RCPT DATA BDAT RSET NOOP QUIT`
  and a few rarer ones.
- IMAP: `CONNECT CAPABILITY LOGIN AUTHENTICATE SELECT EXAMINE LIST STATUS
  CREATE DELETE RENAME APPEND SEARCH FETCH STORE COPY EXPUNGE CLOSE IDLE NOOP
  LOGOUT`, and the separate commands `UID FETCH`, `UID SEARCH`, `UID STORE`
  and `UID COPY`. Most IMAP clients use the `UID` forms, so a `FETCH` rule
  often never matches.

| Phase | When it runs |
| --- | --- |
| `before` | Before the command is handled. Required for `CONNECT`, `AUTH`, `LOGIN`, `AUTHENTICATE`, `STARTTLS` and `LOGOUT`. |
| `content` | SMTP `DATA`/`BDAT` after the whole message arrives, before it is stored. IMAP `SEARCH`/`FETCH` once per matching message. |
| `after` | After the command succeeded, before the reply is sent. For SMTP `DATA`, after the message was stored. |
| `active` | During IMAP `IDLE`; only `disconnect`. |
| `notification` | For each IMAP `IDLE` update (`EXISTS`, `EXPUNGE`, `FLAGS`); only `drop`. |

Constraints that commonly cause a 400 or a rule that never fires:

- `CONNECT` and `STARTTLS` rules must be server rules: no account is known yet.
- An account `MAIL` rule only fires if the client authenticated. For
  unauthenticated senders, use a server rule with a sender filter.
- A `RCPT` rule only sees the current recipient.

## Filters

All optional; every field given must match exactly.

- `message`: `message_id`, `from`, `to`, `headers` (`{"Header": "value"}`).
  Needs SMTP `content`/`after` for headers, or IMAP `content`.
- `folder`: IMAP commands that name or select a folder.
- `username`: server rules only.
- `notification`: `EXISTS`, `EXPUNGE` or `FLAGS`, with the `notification` phase.

Within an account rule, a filter is often unnecessary: the account already
isolates the test.

## Actions

| `type` | Fields |
| --- | --- |
| `reject` | SMTP: `code` (400–599), optional `enhanced_code`, `message`. IMAP: `status` (`NO` or `BAD`), optional `response_code`, `message`. |
| `delay` | `delay_ms` (0–30000). |
| `disconnect` | Optional `silent` (close without a goodbye) and `message`. In the IDLE `active` phase, optional `after_ms`. |
| `response` | Raw reply text in `response` (`{tag}` expands to the IMAP tag), optional `close`. For malformed replies. |
| `capabilities` | `omit`: list of capabilities to hide from EHLO or CAPABILITY; `after` phase. |
| `reset_uidvalidity` | IMAP `SELECT`/`EXAMINE` `before`, account rule, exact `folder`, `max_hits: 1`. |
| `hide` | Omit a message from IMAP `SEARCH`/`FETCH` results; `content` phase with a message filter. |
| `replace_header` / `replace_body` | Change what IMAP returns for a message, not what is stored; `content` phase with a message filter. |
| `drop` | Suppress an IMAP IDLE update; `notification` phase. |

## Limiting how often a rule applies

- `max_hits`: stop after this many applications.
- `sequence`: `{"steps": ["pass", "pass", "apply"], "repeat": false}` applies
  only on the third match. With `"repeat": true` the pattern cycles. Cannot be
  combined with `probability`.
- `expires_at` (RFC3339) and `probability` (0–1) also exist; avoid
  `probability` in tests because it makes them nondeterministic.

## Examples

Reject logins with a temporary error, as a provider outage would, until the
test deletes the rule:

```json
{"name": "login-outage",
 "trigger": {"protocol": "smtp", "command": "AUTH", "phase": "before"},
 "action": {"type": "reject", "code": 454, "enhanced_code": "4.7.0"}}
```

Many clients, including Python's `smtplib`, retry a rejected login with
another mechanism (`PLAIN`, then `LOGIN`). Each attempt is a separate `AUTH`
command, so `max_hits: 1` would only fail the first mechanism and the login
would still succeed. Leave `max_hits` out and delete the rule when the outage
should end.

Fail the third IMAP login with bad credentials:

```json
{"name": "third-login-fails",
 "trigger": {"protocol": "imap", "command": "LOGIN", "phase": "before"},
 "action": {"type": "reject", "status": "NO", "response_code": "AUTHENTICATIONFAILED"},
 "sequence": {"steps": ["pass", "pass", "apply"]}}
```

Drop the connection in the middle of fetching mail:

```json
{"name": "fetch-drops",
 "trigger": {"protocol": "imap", "command": "UID FETCH", "phase": "before"},
 "action": {"type": "disconnect", "silent": true},
 "max_hits": 1}
```

Store the message but lose the acknowledgement, so a retry sends a duplicate:

```json
{"name": "lost-ack",
 "trigger": {"protocol": "smtp", "command": "DATA", "phase": "after"},
 "action": {"type": "disconnect", "silent": true},
 "max_hits": 1}
```

End IDLE after two seconds to test the client's reconnect:

```json
{"name": "idle-timeout",
 "trigger": {"protocol": "imap", "command": "IDLE", "phase": "active"},
 "filter": {"folder": "INBOX"},
 "action": {"type": "disconnect", "after_ms": 2000},
 "max_hits": 1}
```

Miss the next new-mail notification during IDLE:

```json
{"name": "miss-new-mail",
 "trigger": {"protocol": "imap", "command": "IDLE", "phase": "notification"},
 "filter": {"notification": "EXISTS"},
 "action": {"type": "drop"},
 "max_hits": 1}
```

For network-level failures (latency, bandwidth, resets, outages), put
[Toxiproxy](https://github.com/Shopify/toxiproxy) between the application and
Mailarky and keep calling the HTTP API directly.

Full guide: <https://github.com/johlo/mailarky/blob/main/docs/how-to/fault-injection.md>
