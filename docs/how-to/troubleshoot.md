# Troubleshoot a test

| Symptom | Check |
| --- | --- |
| Outgoing mail appears in `default` | Authenticate SMTP using the test account. Anonymous unclaimed recipients route to `default`. |
| Two tests cannot register the same recipient | Use account credentials for SMTP; authenticated tests may send to the same customer address. Recipient registrations are unique only for anonymous routing. |
| SMTP AUTH is unavailable | Use STARTTLS or set `MAILARKY_SMTP_AUTH_ALLOW_INSECURE=true` for local plaintext tests. Compose enables STARTTLS. |
| TLS verification fails | Trust `testdata/tls/server.crt` and connect as localhost, 127.0.0.1, or mailarky. |
| A fault has zero hits | Check its registry, protocol, exact command including UID prefix, phase, and filters. MAIL account rules require SMTP AUTH. |
| A retry still fails | Inspect `hits`, `max_hits`, `enabled`, probability, expiry, and other rules in the same registry. |
| A body fetch changed read state | BODY sets `\Seen`; use BODY.PEEK or EXAMINE for observational reads. HTTP GET is observational. |
| A selected client sees pending deletion later | Non-UID FETCH, STORE, and SEARCH defer EXPUNGE. NOOP, UID commands, or IDLE flush updates. |
| Seeded history disappears | Check opt-in retention limits; they span all folders within an account. Defaults are unlimited. |
| SMTP fails after an account was deleted | Its registered recipients are retired until reassigned. Create a new account. |
| An API request gets 404 | Use `/api/v1/accounts/{id}/...`, including `/default/` for the default account. There are no legacy aliases. |
| A container cannot bind a host port | Set the corresponding `MAILARKY_*_PORT` variable before running Compose. |

Use `docker compose logs mailarky` for service errors. For TCP resets, bandwidth,
or network latency scenarios, check the Toxiproxy configuration separately from
Mailarky's [protocol faults](fault-injection.md).
