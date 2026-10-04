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

Create with optional `name`, `username`, `password`, and `recipients` fields.
Omitted credentials and recipient addresses are generated. Names, usernames,
and explicitly registered recipients must be unique. Recipients are exact,
case-insensitive envelope addresses used only for anonymous SMTP routing.
Passwords have a 72-byte limit. `default` cannot be deleted.

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

MIME header keys retain their original casing. Attachment metadata contains
`part_id`, `filename`, `content_type`, `content_id`, and `size`. Payloads are
decoded from raw MIME when requested. GET endpoints never mark messages read.

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
| GET | `/{name}` | Rule including hit count |
| PUT | `/{name}` | Create or replace definition, preserving existing hits |
| PATCH | `/{name}` | Set `{"enabled":false}` or `true` |
| DELETE | `/{name}` | Remove; 204 |

A rule requires `name`, `trigger`, and `action`. `enabled` defaults to true.
`filter` is optional. `max_hits: 0` means unlimited, `probability` defaults to 1,
and `expires_at` is an optional RFC3339 timestamp. Hits are server-owned and
claimed atomically before actions execute. Unknown action options are rejected.
See the [fault guide](../how-to/fault-injection.md) and [OpenAPI](../../openapi.yaml).

## Notifications and metrics

WebSocket events are account-local `new`, `update`, or `delete` notifications.
Slow subscribers are disconnected when their bounded queue fills. Webhooks
receive a message summary after append, with `Mailarky-Mailbox` and optional
`Mailarky-Label` headers. Delivery is asynchronous and retries up to three times.

`GET /metrics` is available when `MAILARKY_ENABLE_METRICS=true`. Metrics use the
`mailarky_` prefix and an `account` label. See [configuration](configuration.md).
