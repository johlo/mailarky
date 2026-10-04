# HTTP API reference

The API uses `/api/v1`. Every account, including `default`, uses the same route
pattern. `api_base` returned on creation is `/api/v1/accounts/{id}`, prefixed by
`MAILARKY_WEBROOT` when configured. Object fields use snake_case. Errors are
JSON objects such as `{"error":"account not found"}`.

Optional HTTP Basic authentication protects the control API and metrics.
`GET /healthz` is unauthenticated. The API is for a test harness that controls
all accounts, not for account-level access control.

## Accounts

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/v1/accounts` | Account descriptions, without passwords |
| POST | `/api/v1/accounts` | Create account; 201 includes its password once |
| GET | `/api/v1/accounts/{account}` | Account description |
| DELETE | `/api/v1/accounts/{account}` | Delete account and storage; 204 |
| GET | `/api/v1/info` | Service name, protocols, account count |

Create with optional `name`, `username`, `password`, `recipients`, `limits`, and
`oauth_tokens` fields.
Omitted credentials and recipient addresses are generated. Names, usernames,
and explicitly registered recipients must be unique. Recipients are exact,
case-insensitive envelope addresses used only for anonymous SMTP routing.
Passwords have a 72-byte limit. `default` cannot be deleted.

`limits` accepts `smtp_connections`, `imap_connections`, `send_messages`,
`send_window`, `quota_messages`, and `quota_bytes`. Descriptions include the
effective `limits`. See [enforced limits](configuration.md#enforced-limits)
for defaults and rejection behavior. Limits are fixed when the account is created.

## Account resources

Paths below are relative to `/api/v1/accounts/{account}`.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/messages` | Searchable, paginated summaries |
| POST | `/messages` | Seed JSON or raw MIME; 201 message detail |
| DELETE | `/messages` | Delete all, a query, or specified IDs; 204 |
| GET | `/messages/{id}` | Message detail, without changing flags |
| PATCH | `/messages/{id}` | Replace flags using `{"flags":["\\Seen"]}` |
| DELETE | `/messages/{id}` | Idempotent deletion; 204 |
| GET | `/messages/{id}/raw` | Original `message/rfc822` bytes |
| GET | `/messages/{id}/headers` | MIME header names and value arrays |
| GET | `/messages/{id}/parts/{part}` | Decoded attachment/inline part |
| GET | `/messages/{id}/view` | Sandboxed HTML; `?format=text` selects text |
| GET | `/folders` | Name, message count, UIDNEXT, UIDVALIDITY |
| POST | `/folders` | Create with `{"name":"History"}`; 201 |
| GET | `/events` | WebSocket stream of `{"type":...,"data":...}` |
| PUT | `/oauth-tokens` | Replace configured test tokens; `{"tokens":[...]}`; 204 |

MIME header keys retain their original casing. Attachment metadata contains
`part_id`, `filename`, `content_type`, `content_id`, and `size`. Payloads are
decoded from raw MIME when requested. GET endpoints never mark messages read.

### Test OAuth tokens

Account creation accepts an `oauth_tokens` array. PUT `/oauth-tokens` accepts
`{"tokens":[...]}` and replaces the full set; `{"tokens":[]}` revokes all.
This endpoint also works for `default`. Each array entry has required `token`
(1–1,024 printable ASCII bytes without spaces), optional `status` (`valid`,
`expired`, or `rejected`; default `valid`), and optional `expires_at` (RFC3339).
There can be at most 100 unique token values per account.

Tokens are scoped to the exact account username. Valid tokens fail once their
expiry is reached. Unknown tokens fail as rejected. Token hashes, statuses,
and expiry are persisted with account storage; plaintext token values are
never returned or persisted. Replacement is atomic and affects future
authentication only, not sessions already authenticated. A token may have a
different state in a different account.

Both protocols advertise XOAUTH2 where authentication is available. A valid
token follows the same ownership, connection-limit, and account-fault paths as
password authentication. A failed token gets a base64 JSON `401` bearer
challenge, then SMTP `535 5.7.8` or IMAP `NO [AUTHENTICATIONFAILED]` after the
client's empty response. Expired tokens use IMAP `NO [EXPIRED]`. Malformed
XOAUTH2 payloads fail immediately. Authentication fault rules can override
these replies. There is no OAuth authorization/token endpoint or provider JWT
validation; see [OAuth testing](../how-to/integrate-with-application.md#test-oauth-authentication).

### Message creation

Use `Content-Type: application/json` for a fixture:

```json
{
  "folder": "INBOX",
  "from": "Alice <alice@customer.test>",
  "to": ["app@example.test"],
  "subject": "Earlier reply",
  "body": "Hello",
  "message_id": "history@example.test",
  "date": "2020-01-02T03:04:05Z",
  "internal_date": "2021-02-03T04:05:06Z",
  "flags": []
}
```

`from` and at least one `to` are required. `cc` and `bcc` accept address arrays.
`folder` defaults to `INBOX` and must exist. Dates default to now; a supplied
`date` also supplies the internal date unless `internal_date` is given.
A Message-ID is generated when omitted. Unknown JSON fields are rejected.

Use `Content-Type: message/rfc822` with the `.eml` bytes to import arbitrary MIME.
`?folder=Sent` selects the folder; the default is `INBOX`. Both formats return
message detail with `id`, `message_id`, `folder`, and `uid`.
Exceeding the account's storage quota returns HTTP 507 without storing or
evicting any message.

### Search and deletion

`GET /messages?query=...&start=0&limit=50&tz=UTC` returns `messages`, `start`,
`total`, `unread`, `matched`, and `matched_unread`. Results are newest first.
The maximum page size is 10,000. Query terms are ANDed; quoted phrases and
`!` or `-` negation are supported.

Filters: `from:`, `to:`, `cc:`, `bcc:`, `reply-to:`, `addressed:`, `subject:`,
`message-id:`, `username:`, `folder:`, `body:`, `is:read`, `is:unread`,
`has:attachment`, `has:inline`, `larger:`, `smaller:`, `before:`, `after:`.
Bare terms search text, subject, addresses, and Message-ID. Matching is
case-insensitive substring matching; fault message filters use exact matching.

`DELETE /messages?query=subject:temporary` searches and deletes in one committed
mutation. Alternatively send `{"ids":["id-1","id-2"]}`; already absent IDs are
ignored. Omitting both query and IDs deletes all messages in that account.

## Faults

Account faults live at `/api/v1/accounts/{account}/faults`; server faults live
at `/api/v1/faults`. These are independent registries with identical operations.

| Method | Suffix | Result |
| --- | --- | --- |
| GET | empty | Rules in creation order |
| POST | empty | Create; 201 |
| GET | `/{name}` | Rule including `matches` and `hits` counters |
| PUT | `/{name}` | Create or replace definition, preserving counters and sequence position |
| PATCH | `/{name}` | Set `{"enabled":false}` or `true` |
| DELETE | `/{name}` | Remove; 204 |

A rule requires `name`, `trigger`, and `action`. `enabled` defaults to true.
`filter` is optional. `max_hits: 0` means unlimited, `probability` defaults to 1,
and `expires_at` is an optional RFC3339 timestamp. Hits are server-owned and
claimed atomically before actions execute. `matches` counts eligible events
before applying probability or sequence selection. `sequence` contains
`steps` (1–1,024 `pass`/`apply` strings) and optional `repeat` (default false).
It cannot be combined with probability. Unknown action options are rejected.
See the [fault guide](../how-to/fault-injection.md) and [OpenAPI](../../openapi.yaml).

## Notifications and metrics

WebSocket events are account-local `new`, `update`, or `delete` notifications.
Slow subscribers are disconnected when their bounded queue fills. Webhooks
receive a message summary after append, with `Mailarky-Mailbox` and optional
`Mailarky-Label` headers. Delivery is asynchronous and retries up to three times.

`GET /metrics` is available when `MAILARKY_ENABLE_METRICS=true`. Metrics use the
`mailarky_` prefix and an `account` label. See [configuration](configuration.md).
