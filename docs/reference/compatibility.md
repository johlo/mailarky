# Headless feature compatibility

This is an independent implementation against documented Mailpit interfaces,
plus IMAP and message-scoped toxics. It does not embed or fork Mailpit. The scope
excludes POP3 and the browser mail client/UI. Feature availability does not imply
byte-for-byte implementation or configuration compatibility.

| Capability | Implementation |
| --- | --- |
| SMTP capture | ESMTP, UTF-8, configurable size limit, PLAIN/LOGIN auth, optional/required STARTTLS or implicit TLS |
| Shared mailbox | HTTP + TLS IMAP, folders, raw MIME, flags, attachments and envelope metadata |
| Message API | List/search/detail/headers/raw/parts/thumbnails, read/unread, delete, latest alias |
| Send/import | JSON composition with MIME attachments, raw import, fixtures, IMAP APPEND, sendmail client |
| Search | AND/quoted phrases/negation, address/header/body, tag, read, attachment, size/date filters and timezone |
| Tags | CRUD, X-Tags, plus-addressing, filter file/expressions, username, optional title casing |
| Storage | In-memory default or durable bbolt; count/age retention; duplicate-ID suppression; raw .eml dumps |
| Notifications | /api/events WebSocket and optional HTTP webhooks with retry/delay/rate limit |
| Relay | Explicit release with recipient restrictions/overrides; automatic relay all or matching recipients |
| Forwarding | Copy accepted messages to configured recipients through another SMTP server |
| Diagnostics | Link validation, Can I Email HTML/CSS compatibility analysis, SpamAssassin integration, unsubscribe metadata |
| Operational controls | HTTP TLS/auth, separate send credentials, SMTP auth/TLS, CORS, allowed hosts, webroot, health and Prometheus metrics |
| Failure simulation | Exact message-scoped SMTP reject/delay, IMAP delay/hide/header mutation; CRUD, enable/disable, probability/hit limits |
| POP3 / browser client | Excluded |

## Deliberate differences

- HTTP listens on 8026, not Mailpit's conventional 8025. There is no UI at `/`;
  it returns service metadata. `/api/v1/webui` returns capabilities only.
- SMTP mail goes into Sent by default, configurable to another existing folder.
- Global chaos updates/options fail explicitly. Connection-wide/authentication
  failures cannot isolate individual messages and are not available as toxics.
- Toxics are ephemeral. There is no global reset endpoint for them.
- bbolt files are not compatible with Mailpit SQLite databases. Move message MIME
  via `/api/v1/message/{id}/raw` and `/api/v1/messages/raw` instead.
- API IDs are UUIDs. HTTP Created is ingestion time; Date is the original header.
  Folder and UID are additional response fields.
- The HTML checker detects features heuristically from DOM/CSS and scores the
  bundled Can I Email records equally per client/version. It does not reproduce
  Mailpit's detector/scoring algorithm or render screenshots. Unknown/unmatched
  CSS may not be diagnosed. Its response includes the data timestamp and scoring
  method; check real target email clients when rendering fidelity matters.
- IMAP is designed for polling. IDLE/unsolicited updates and implicit Seen on
  body FETCH are not implemented. Explicit STORE and HTTP read updates work.
- Search validation returns 400 for unknown/malformed filters. Metrics use the
  `mail_emulator_` prefix. Only documented settings are accepted.
- Sendmail mode is a local unauthenticated SMTP submission client; use a normal
  SMTP client for authenticated submission.
- Retention is mailbox-wide. Disable it in a shared concurrent test suite, and
  clean up by owned IDs. Webhook queues are bounded and not persisted.

Public contract references: [Mailpit API](https://mailpit.axllent.org/docs/api-v1/),
[search](https://mailpit.axllent.org/docs/usage/search-filters/),
[tagging](https://mailpit.axllent.org/docs/usage/tagging/),
[Can I Email data](https://www.caniemail.com/api/data.json).
The repository's [OpenAPI](../../openapi.yaml) documents this implementation.
