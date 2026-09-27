# Troubleshoot

| Symptom | Check |
| --- | --- |
| Port already in use | Set MAIL_SANDBOX_SMTP_PORT, MAIL_SANDBOX_IMAP_PORT and MAIL_SANDBOX_HTTP_PORT for Compose |
| TLS verification fails | Trust testdata/tls/server.crt; connect as localhost, 127.0.0.1 or mail-sandbox |
| No SMTP delivery in INBOX | SMTP defaults to Sent; configure MAIL_SANDBOX_SMTP_FOLDER if needed |
| Root API does not show a provisioned account's mail | Prefix message paths with that account's returned api_base; root paths show only default |
| SMTP delivery goes to default instead of a provisioned account | Match an assigned envelope recipient exactly, ignoring case; To headers and SMTP usernames do not select accounts |
| SMTP RCPT returns 553 | Recipients belong to different accounts; use a separate transaction for each account |
| SMTP RCPT returns 550 after account deletion | Deleted recipient bindings remain unavailable until explicitly reassigned to a new account |
| Mail disappears | In-memory restart, count retention (500 default), age retention, or explicit deletion |
| App cannot authenticate | Match configured IMAP credentials or SMTP/HTTP auth file; SMTP auth normally requires TLS |
| Generated account credentials fail over HTTP or SMTP | They are IMAP credentials; HTTP and SMTP use service-wide authentication settings |
| Toxic rejected with 400 | Supply an exact nonempty selector; pre-DATA stages only know envelope addresses |
| Toxic affects another test | Check for reused test tokens, shared recipient selectors or combined protocol commands |
| Toxic did not fire | Check enabled, hits/max_hits, probability, folder and exact selector values |
| Global chaos rejected | Migrate to /api/v1/toxics; global failures violate concurrency isolation |
| Stored MIME is unchanged by IMAP toxic | Expected: mutation is confined to the IMAP response |
| IMAP does not push new mail | Poll SELECT/STATUS/UID SEARCH; IDLE notifications are not implemented |
| Link/CSS checker rejects localhost | Set MP_ALLOW_INTERNAL_HTTP_REQUESTS=true for deliberate local fixtures |
| Spam check unavailable | Configure MP_SPAMASSASSIN as a reachable spamd host:port |
| Database will not open | Use a new bbolt file, a writable directory and one owning process |
| Relay fails | Check explicit configuration, recipients, credentials, TLS trust and peer logs |

Use `docker compose logs mail-sandbox`, `/api/v1/info`, and `/api/v1/toxics` to
inspect state. Prefix inspection paths with `api_base` for a provisioned account;
`/api/v1/mailboxes` lists accounts. `/healthz` checks HTTP responsiveness, not an application's sync
completion. Wait for the consuming application's result in end-to-end tests.
