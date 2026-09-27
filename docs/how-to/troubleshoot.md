# Troubleshoot

Start with `docker compose logs mail-sandbox`, `GET /api/v1/info`,
`GET /api/v1/mailboxes` and `GET /api/v1/toxics`.

## Delivery and routing

| Symptom | Fix |
| --- | --- |
| SMTP mail isn't in `INBOX` | It goes to `Sent`. Set `MAIL_SANDBOX_SMTP_FOLDER=INBOX` if needed. |
| Mail went to the default account | Routing uses the exact **envelope** recipient, not the `To` header. |
| API doesn't show an account's mail | Prefix the path with the account's `api_base`. |
| RCPT returns 553 | The recipients belong to different accounts. Use one transaction per account. |
| RCPT returns 550 | The address belonged to a deleted account. |
| Mail disappeared | Restart without persistence, retention (500 messages by default), or a delete. |

## Connections

| Symptom | Fix |
| --- | --- |
| Port already in use | Set `MAIL_SANDBOX_SMTP_PORT`, `_IMAP_PORT`, `_HTTP_PORT`. |
| TLS verification fails | Trust `testdata/tls/server.crt`, and connect as `localhost`, `127.0.0.1` or `mail-sandbox`. |
| IMAP login fails | Check the credentials. Account passwords work for IMAP only. |
| IMAP doesn't show new mail | IDLE isn't supported, so poll with STATUS or UID SEARCH. |
| Database won't open | Check that the directory is writable and no other process uses the file. |

## Toxics

| Symptom | Fix |
| --- | --- |
| Create returns 400 | The selector is empty or too broad, or it uses headers at an SMTP stage before `data`. |
| Toxic didn't fire | Check `enabled`, `hits`/`max_hits`, `probability` and exact selector values. |
| Toxic hit another test | Tests share a token or address, or send their mail in one command. |
| Stored mail unchanged by an IMAP toxic | Expected. IMAP toxics change only the response. |

## Other

| Symptom | Fix |
| --- | --- |
| Link or CSS check rejects localhost | Set `MP_ALLOW_INTERNAL_HTTP_REQUESTS=true`. |
| Spam check unavailable | Set `MP_SPAMASSASSIN` to a reachable spamd `host:port`. |
| Relay fails | Check the relay config, recipients, credentials and TLS trust. |

`/healthz` only shows that the sandbox is up. In end-to-end tests, wait for
your application's result instead.
