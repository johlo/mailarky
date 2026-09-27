# HTTP API

Base URL `http://localhost:8026`. The full contract is [openapi.yaml](../../openapi.yaml).

Paths address the default account. For a [separate mailbox](../how-to/separate-mailboxes.md),
prefix them with its `api_base`, e.g. `/mailboxes/{id}/api/v1/messages`.

## Common endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/search?query=…` | Find messages (newest first) |
| `GET /api/v1/messages` | List messages (newest first) |
| `GET /api/v1/message/{id}` | Details: headers, decoded Text/HTML, attachments. Marks it read. |
| `GET /api/v1/message/{id}/raw` | Original MIME |
| `POST /messages` | Add a fixture ([seed test data](../how-to/seed-test-scenarios.md)) |
| `DELETE /api/v1/messages` | Delete `{"IDs": [...]}`. **An empty list deletes all.** |
| `POST/GET /api/v1/mailboxes` | Create or list accounts |
| `DELETE /api/v1/mailboxes/{id}` | Delete an account |
| `/api/v1/toxics[/{name}]` | Manage [toxics](../how-to/toxics.md) |

`{id}` is the message's UUID, or `latest`.

## All endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /`, `/healthz`, `/livez`, `/readyz` | Service info and health |
| `PUT /api/v1/messages` | Mark read/unread by `IDs` or `Search` (all if neither) |
| `DELETE /api/v1/search?query=…` | Delete matches (query required) |
| `GET /api/v1/message/{id}/headers` | Headers as a map of arrays |
| `GET /api/v1/message/{id}/part/{part}[/thumb]` | Attachment bytes, or a PNG thumbnail |
| `GET /api/v1/message/{id}/link-check` | Check links (`follow=true` to follow redirects) |
| `GET /api/v1/message/{id}/html-check` | HTML/CSS email client compatibility |
| `GET /api/v1/message/{id}/sa-check` | SpamAssassin report (if configured) |
| `POST /api/v1/message/{id}/release` | Deliver through the configured relay |
| `POST /api/v1/send` | Build and store a message from JSON |
| `POST /api/v1/messages/raw?folder=…` | Import raw MIME |
| `GET/PUT /api/v1/tags`, `PUT/DELETE /api/v1/tags/{tag}` | List, set, rename and remove tags |
| `GET/POST /api/v1/folders` | List or create folders |
| `GET /api/v1/info` | Store and runtime info |
| `GET /api/events` | WebSocket stream of new, updated and deleted messages |
| `GET /metrics` | Prometheus metrics (if enabled) |
| `GET /view/{id}.html`, `.txt` | Message body |

## Search

```
to:alice@example.test subject:"appointment" !is:read after:2024-01-01
```

- Terms are combined with AND. Quote phrases. Prefix `!` or `-` to negate.
- Filters: `from`, `to`, `cc`, `bcc`, `reply-to`, `addressed`, `subject`,
  `message-id`, `tag`, `username`, `folder`, `body`, `is:read|unread|tagged`,
  `has:attachment|inline`, `larger:`/`smaller:` (e.g. `2MB`), `before:`/`after:`.
- Dates refer to arrival time. Pass `tz` to choose a time zone (default UTC).
- Paging: `start` (default 0), `limit` (default 50, max 10000).
- Matching ignores case. Invalid queries return 400. URL-encode the query.

## Message fields

List results contain `messages`, `total` and `unread`. Each message has `ID`,
`MessageID`, `From`, `To`, `Cc`, `Bcc`, `ReplyTo`, `Subject`, `Created`
(arrival time), `Size`, `Tags`, `Read`, plus `MailboxID`, `Folder` and `UID`.
Details add `Date`, `Text`, `HTML`, `Inline` and `Attachments`.

`ID` (a UUID), `MessageID` (the header) and `UID` (IMAP) are different
identifiers. Don't mix them up.

## Errors

400 validation, 401 authentication, 404 not found, 500 storage. Unknown JSON
fields are rejected. `POST /api/v1/send` returns JSON `{"Error": …}` and uses
502 when onward delivery fails after the message was stored.
