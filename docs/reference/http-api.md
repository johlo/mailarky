# HTTP fixture API reference

Default base URL: `http://localhost:8026` on the host, or
`http://imap-emulator:8026` on the Compose network. The API has no authentication
and creates local test fixtures. It never sends mail to recipients.

The [OpenAPI specification](../../openapi.yaml) is the machine-readable
contract. Implementation: [control.go](../../control.go).

## GET /healthz

Returns HTTP 200 with the text body `ok` followed by a newline. It checks that
the HTTP service can respond; it does not authenticate to IMAP or wait for an
application sync.

## POST /messages

Appends a synthetic email to one folder. Send a JSON object with
`Content-Type: application/json`. The decoder limits the request to 1 MiB and
rejects unknown fields.

| Field | JSON type | Requirement or default | Meaning |
| --- | --- | --- | --- |
| `folder` | string | Omitted or empty: `INBOX` | Exactly `INBOX`, `Sent`, or `Archive` |
| `from` | string | Required | One parseable sender address, optionally with a display name |
| `to` | array of strings | At least one recipient across `to`, `cc`, and `bcc` | To addresses |
| `cc` | array of strings | Empty by default | Cc addresses |
| `bcc` | array of strings | Empty by default | Bcc addresses, retained in the synthetic message |
| `subject` | string | Empty by default | Single-line subject; non-ASCII text is MIME encoded |
| `message_id` | string | Omitted or empty: generated UUID plus `@imap.example.test` | Message-ID header value; one surrounding angle-bracket pair is removed before storage formatting and response |
| `date` | string or null | Omitted or null: current UTC time | RFC 3339 original message time, written to the Date header in UTC at second precision |
| `internal_date` | string or null | Omitted or null: resolved `date` | RFC 3339 IMAP INTERNALDATE |
| `body` | string | Empty by default | UTF-8 plain-text message body, retrievable through IMAP |
| `flags` | array of strings | Empty by default | Flags assigned at creation, such as `"\\Seen"` in JSON |

Each address entry is parsed individually. Addresses and subjects must not
contain CR or LF. After normalization, Message-IDs must be nonempty and must
not contain angle brackets, spaces, tabs, CR, or LF. The service does not
require Message-IDs to be unique or validate that they contain `@`.

The generated message contains address headers, Subject, Message-ID, Date,
MIME-Version, and `Content-Type: text/plain; charset=utf-8`. There are no HTTP
fields for arbitrary headers, HTML alternatives, or attachments.

### Successful response

HTTP 201, `Content-Type: application/json`:

```json
{"folder":"INBOX","message_id":"example@example.test"}
```

The returned ID omits angle brackets; the stored header includes them. The
response does not contain the IMAP UID. The append has completed when 201 is
returned, but a connected application may not have polled yet.

Every successful POST creates a separate message. Reusing a Message-ID does
not replace or merge a previous fixture. There are no HTTP endpoints to list,
fetch, update, delete, or reset messages; use IMAP to inspect them.

## Errors

Errors use plain-text bodies ending in a newline.

| HTTP status | Body | Condition |
| --- | --- | --- |
| 400 | `invalid message JSON` | Decode failure, such as malformed JSON, an unknown field, an invalid field type or date, or exceeding the request limit |
| 400 | `folder must be INBOX, Sent or Archive` | Unsupported folder |
| 400 | `invalid From address`, `invalid To address`, `invalid Cc address`, or `invalid Bcc address` | An address cannot be parsed or contains a newline |
| 400 | `recipients are required and subject must be a single line` | No recipients, or a subject containing a newline |
| 400 | `invalid message_id` | Message-ID fails validation after normalization |
| 500 | `could not append message` | Mailbox append failed |

Rejected fixtures are not appended. There is no authentication challenge or
application-import status endpoint.

See [seed test scenarios](../how-to/seed-test-scenarios.md) for requests you
can adapt to tests.

[Documentation index](../README.md)
