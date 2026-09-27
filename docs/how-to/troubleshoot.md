# Troubleshoot

| Symptom | Check |
| --- | --- |
| Port already in use | Set SMTP_EMULATOR_PORT, IMAP_EMULATOR_PORT and IMAP_EMULATOR_HTTP_PORT for Compose |
| TLS verification fails | Trust testdata/tls/server.crt; connect as localhost, 127.0.0.1 or imap-emulator |
| No SMTP delivery in INBOX | SMTP defaults to Sent; configure SMTP_EMULATOR_FOLDER if needed |
| Mail disappears | In-memory restart, count retention (500 default), age retention, or explicit deletion |
| App cannot authenticate | Match configured IMAP credentials or SMTP/HTTP auth file; SMTP auth normally requires TLS |
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

Use `docker compose logs imap-emulator`, `/api/v1/info`, and `/api/v1/toxics` to
inspect state. `/healthz` checks HTTP responsiveness, not an application's sync
completion. Wait for the consuming application's result in end-to-end tests.
