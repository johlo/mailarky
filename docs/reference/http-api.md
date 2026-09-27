# HTTP API reference

Default base URL `http://localhost:8026`. The complete machine-readable contract
is [openapi.yaml](../../openapi.yaml). Authentication/TLS, webroot, CORS and host
restrictions are optional; see [configuration](configuration.md).

Create/list accounts at `POST/GET /api/v1/mailboxes`; inspect/delete at
`GET/DELETE /api/v1/mailboxes/{id}`. Creation returns credentials once and an
`api_base` such as `/mailboxes/{id}`. Prefix any data/toxic endpoint below with
that base to operate on the account. Root paths address default only. See
[separate mailboxes](../how-to/separate-mailboxes.md) for the lifecycle contract.

## Endpoints

| Method / path | Purpose |
| --- | --- |
| GET /, /healthz, /livez, /readyz | Service metadata / health |
| GET /api/v1/messages, /api/v1/search | Paginated messages, newest ingestion first |
| PUT /api/v1/messages | Set Read for IDs or Search, or all if neither provided |
| DELETE /api/v1/messages | Delete IDs; absent/empty IDs deletes all |
| DELETE /api/v1/search | Delete a required nonempty query |
| GET /api/v1/message/{id} | Detail, decoded Text/HTML; marks read |
| GET /api/v1/message/{id}/headers | Header map of arrays |
| GET /api/v1/message/{id}/raw | Original MIME bytes |
| GET /api/v1/message/{id}/part/{part} | Decoded attachment/inline bytes |
| GET /api/v1/message/{id}/part/{part}/thumb | PNG thumbnail, max 320px dimensions |
| POST /api/v1/message/{id}/release | Deliver through configured relay to To recipients |
| GET /api/v1/message/{id}/link-check | Validate links; optional follow=true |
| GET /api/v1/message/{id}/html-check | HTML/CSS compatibility analysis |
| GET /api/v1/message/{id}/sa-check | Configured SpamAssassin report |
| POST /api/v1/send | Compose/capture JSON Text/HTML, headers, tags and base64 attachments |
| POST /api/v1/messages/raw?folder=INBOX | Import MIME bytes |
| POST /messages | Convenient historical plain-text fixture |
| GET/PUT /api/v1/tags | List tags / replace tags for IDs |
| PUT/DELETE /api/v1/tags/{tag} | Rename with Name / remove globally |
| GET /api/v1/info, /api/v1/webui | Store/runtime metadata / headless capabilities |
| GET /api/events | WebSocket new/update/delete notifications |
| GET /metrics | Optional Prometheus metrics |
| GET/POST /api/v1/folders | List folders / create with name |
| GET/POST /api/v1/toxics | List / create scoped toxics |
| GET/PUT/PATCH/DELETE /api/v1/toxics/{name} | Inspect / replace / enable-disable / delete |
| GET/PUT /api/v1/chaos | Disabled metadata / explicit 400 rejection |
| GET /view/{id}.html or .txt | Message body; HTML is sandboxed, no mail client UI |

Message endpoints accept database UUIDs or `latest`. List responses use
`messages`, `start`, `total`, `unread`, `messages_count`, `messages_unread`, `tags`.
Individual message fields retain uppercase Mailpit-style casing, including ID,
MessageID, From/To/Cc/Bcc/ReplyTo, Subject, Created, Size, Tags, Read, and Username.
The additional MailboxID, Folder and UID fields identify the IMAP record. Detail adds Date,
Text, HTML, Inline, Attachments, ReturnPath and ListUnsubscribe. Attachments expose
PartID, FileName, ContentType, ContentID, Size and MD5/SHA1/SHA256 Checksums.

## Search

Queries combine words/quoted phrases with AND; prefix a term with `!` or `-` to
negate it. Supported filters: from, to, cc, bcc, reply-to, addressed, subject,
message-id, tag, username, folder, body; is:read/unread/tagged; has:attachment/inline;
larger/smaller (bytes, K/KB, M/MB); before/after (ISO or YYYY/MM/DD, optionally time).
Date filters use ingestion time. `tz` chooses an IANA timezone (UTC default).
`start` defaults to 0; `limit` defaults to 50, maximum 10000. Filters ignore case
for text matching; malformed queries return 400. URL-encode query strings.

## Fixture body

`POST /messages` accepts at most 1 MiB JSON. Required: from and at least one
recipient in to/cc/bcc arrays. Optional: folder (INBOX), subject, message_id
(generated if absent), date (now), internal_date (date), body, flags. Header fields
reject newlines. Addresses allow display names. Date values are RFC3339. Folder
must exist. The 201 response contains folder and message_id. It preserves the
original fixture contract; use raw import or send for custom headers/attachments.

## Sending and mutation

`POST /api/v1/send` accepts From {Name,Email}, To/Cc/ReplyTo arrays of that shape,
Bcc strings, Subject, Text, HTML, Headers (string map), Tags and Attachments
[{Filename,ContentType,ContentID,Content(base64)}]. At least one recipient is
required. A successful response has ID and MessageID. Capture may optionally
relay/forward if configured. It does not execute SMTP toxics; those act on SMTP.

Read mutation uses {IDs:[...],Read:true} or {Search:"...",Read:false}. Tags use
{IDs:[...],Tags:[...]}; rename uses {Name:"..."}. Delete uses {IDs:[...]}. Never
omit ownership IDs in shared concurrent tests. Global tag operations likewise
require test-owned tag names.

Most errors are plain text and use 400 for validation, 401 for authentication,
404 for missing resources and 500 for storage failures. Send errors use JSON
{Error:...}; external delivery failure returns 502 after capture. JSON decoding
rejects unknown fields and trailing values. See [toxics](../how-to/toxics.md) for
failure configuration and [compatibility](compatibility.md) for differences.
