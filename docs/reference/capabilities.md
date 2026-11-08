# Capabilities

| Area | Supported |
| --- | --- |
| SMTP capture | Anonymous recipient routing and fan-out; authenticated account ownership; PLAIN/LOGIN/XOAUTH2; optional STARTTLS or implicit TLS; DATA and BDAT |
| Accounts | Create/list/delete; shared SMTP/IMAP credentials; isolated folders, storage, UIDs, rules, and events |
| IMAP | TLS IMAP4rev1; folders, SEARCH/FETCH and UID variants, flags, APPEND, COPY, EXPUNGE, IDLE updates |
| Fixtures | JSON message builder, raw MIME import, IMAP APPEND, historical Date and INTERNALDATE; bulk mbox and Maildir import with Gmail label mapping |
| Inspection | Side-effect-free HTTP GET; search, pagination, raw MIME, headers, attachment parts, sandboxed HTML/text |
| Faults | Explicit SMTP/IMAP trigger, optional filters, typed action; separate registries; deterministic pass/apply sequences, hit limits, probability, expiry; timed active IDLE disconnects and notification drops |
| Rate-limit testing | Independent per-account SMTP/IMAP command windows shared across authenticated connections; SMTP `451 4.7.0` and IMAP `NO [LIMIT]` on overload; recovery after expiry; HTTP remains available |
| Enforced limits | SMTP recipients per transaction; per-account authenticated connections, successful SMTP submission windows, message and byte quotas |
| Test OAuth | Account-specific XOAUTH2 tokens for SMTP/IMAP, valid/expired/rejected states, expiry and rotation; no provider HTTP OAuth service |
| State | Memory or bbolt; immutable raw MIME; stable persisted UID identity; opt-in per-account retention |
| Observability | Account WebSocket events, asynchronous webhooks, optional Prometheus metrics |
| Webmail | Optional Roundcube Compose profile using authenticated SMTP and TLS IMAP |

The service has no mail relay, forwarding, POP3, built-in webmail, tags, HTML
compatibility checks, link checks, SpamAssassin integration, thumbnails, or
separate send API. Seed raw MIME or use SMTP to create messages.

See [rate-limit testing](../how-to/test-rate-limits.md) for client backoff checks,
[IMAP behavior](imap-behavior.md) for protocol limits, and
[configuration](configuration.md) for settings. Combine with
[Toxiproxy](https://github.com/Shopify/toxiproxy) for TCP faults.
