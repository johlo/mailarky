# Use message-scoped toxics in concurrent tests

Give each test a unique token, preferably a UUID, and include it in an
`X-Test-ID` header or Message-ID. A unique envelope recipient is useful when the
application cannot add headers. Scope both message assertions and cleanup to
that token. Never clear the whole mailbox or delete other tests' toxics.

## Create, disable, enable, inspect and delete

The following toxic rejects only mail carrying `X-Test-ID: run-a-unique-token`
with a temporary SMTP failure after DATA. Replace the token for every test.

```sh
curl -fsS http://localhost:8026/api/v1/toxics -H 'Content-Type: application/json'   -d '{"name":"run-a-reject","type":"smtp_reject","enabled":true,"selector":{"headers":{"X-Test-ID":"run-a-unique-token"}},"attributes":{"stage":"data","code":451},"max_hits":1}'

curl -fsS -X PATCH http://localhost:8026/api/v1/toxics/run-a-reject   -H 'Content-Type: application/json' -d '{"enabled":false}'
curl -fsS -X PATCH http://localhost:8026/api/v1/toxics/run-a-reject   -H 'Content-Type: application/json' -d '{"enabled":true}'
curl -fsS http://localhost:8026/api/v1/toxics/run-a-reject
curl -fsS -X DELETE http://localhost:8026/api/v1/toxics/run-a-reject
```

Create defaults to enabled. `PUT /api/v1/toxics/{name}` replaces the definition
(and creates it when absent). PATCH changes only `enabled`. PUT/PATCH preserve
`hits`; delete and recreate to reset them. Unique names prevent collisions.
Cleanup belongs in the test's `finally`/defer block. Disabling/deleting prevents
future matches; a delay already claimed is allowed to finish.

## Select a message

All supplied selector fields must match:

| Field | Comparison |
| --- | --- |
| `message_id` | Exact Message-ID, ignoring surrounding angle brackets |
| `headers` | Every named header must contain the exact supplied value; header names ignore case |
| `from` | Exact envelope sender, ignoring case; header fallback for fixtures/IMAP APPEND |
| `to` | One exact envelope recipient, ignoring case; header fallback for fixtures/IMAP APPEND |
| `folder` | Exact folder name, an additional restriction |

At least `message_id`, `from`, `to`, or a nonempty header selector is mandatory.
Empty selectors, folder-only selectors and wildcard patterns are rejected.
Use an address unique to the test: choosing a shared clinic address deliberately
matches multiple tests and defeats isolation. A shared message containing several
tests' recipient addresses is also one SMTP transaction, so a DATA rejection
rejects that transaction for all its recipients.

## Choose a toxic

| Type | Attributes | Effect |
| --- | --- | --- |
| `smtp_reject` | `code` 400–599; optional `stage` | Fails matching SMTP transaction; rejected DATA is not stored |
| `smtp_delay` | `delay_ms` 0–30000; optional `stage` | Delays matching SMTP command |
| `imap_delay` | `delay_ms` 0–30000 | Delays matching SEARCH candidates or FETCH records |
| `imap_hide` | none | Omits matching records from SEARCH/FETCH responses |
| `imap_replace_header` | `header`, `value` | Changes the header in returned IMAP content; empty value removes it |

SMTP stages are `sender` (MAIL FROM), `recipient` (RCPT TO), and `data`
(default). Before DATA the server has no headers or Message-ID: `sender`
requires only `from`, and `recipient` accepts only envelope `from`/`to`.
The API rejects impossible early-stage header selectors. Connection/authentication
failures are intentionally excluded because no individual message exists yet.

`probability` is optional, defaults to 1, and ranges from 0 to 1. `max_hits`
defaults to zero (unlimited). Matching/probability selection and hit accounting
are atomic across connections. A hit counts a selected toxic, including a toxic
later short-circuited by an earlier reject/hide in registration order. Tests
needing deterministic behavior should use one toxic per operation and probability 1.

## Understand the isolation boundary

SMTP delays and IMAP delays never hold mailbox or toxic registry locks. Separate
connections continue serving unrelated messages. IMAP SEARCH first narrows the
candidates using the original message, then applies toxics; a search for test B
cannot consume or wait on test A's toxic. Header replacements affect fetched
content and the final search check, but do not make a message searchable under
an injected value that did not match the original candidate search.

A single IMAP command requesting both tests' messages still waits for every
selected message. Use per-test connections and searches/UID sets. `imap_hide`
hides responses, not STATUS counts; toxics never rewrite raw stored mail, affect
HTTP inspection, or delete messages. Toxics are ephemeral and reset on restart,
even when messages are persisted.

The global `/api/v1/chaos` update endpoint and `MP_ENABLE_CHAOS` are rejected.
They cannot meet the isolation contract. See [OpenAPI](../../openapi.yaml) for
request/response schemas.
