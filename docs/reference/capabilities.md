# Capabilities

| Area | What's supported |
| --- | --- |
| SMTP | ESMTP, UTF-8, size limit, optional PLAIN/LOGIN auth, optional STARTTLS or implicit TLS |
| IMAP | TLS IMAP4rev1 with search, fetch, append, flags, copy, expunge and folder management ([details](imap-behavior.md)) |
| Accounts | Default account plus independent accounts created through the API |
| Messages | List, search, details, headers, raw MIME, attachments, thumbnails, read flags, delete |
| Adding mail | SMTP, JSON fixtures, raw MIME import, JSON send with attachments, IMAP APPEND, `sendmail` mode |
| Search | Phrases, negation, address/header/body filters, tags, read state, attachments, size and date ([syntax](http-api.md#search)) |
| Tags | Create, rename, remove; automatic from `X-Tags`, plus-addresses and filter rules |
| Storage | In memory or bbolt on disk; retention by count or age; optional duplicate suppression; `.eml` dumps |
| Notifications | WebSocket stream and webhooks with retry |
| Failure simulation | Per-message SMTP reject/delay and IMAP delay/hide/header change ([toxics](../how-to/toxics.md)) |
| Checks | Link validation, HTML/CSS email client compatibility, SpamAssassin, unsubscribe headers |
| Operations | HTTP TLS and auth, CORS, allowed hosts, path prefix, health probes, Prometheus metrics |

## Not supported

- Web UI or browser mail client (`GET /` returns service info)
- POP3
- IMAP IDLE and push updates
- Global failure modes. Faults always target specific messages.
- Connection-level or authentication failures as toxics
- Several processes sharing one database
- Relay or forwarding. Captured mail is never delivered onward.

## Worth knowing

- SMTP mail is stored in `Sent`, fixtures in `INBOX`.
- Message IDs in the API are UUIDs. `Created` is the arrival time; `Date` is
  the header.
- The HTML compatibility score is a heuristic based on
  [Can I Email](https://www.caniemail.com/) data. It doesn't render the mail,
  so check real clients when appearance matters.
- Metrics use the `mail_sandbox_` prefix.
