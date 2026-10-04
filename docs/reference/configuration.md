# Configuration reference

Settings are applied in this order: defaults, YAML from `MAILARKY_CONFIG`,
environment, command-line flags. Unknown YAML fields and flags are errors.
Environment variables use only the `MAILARKY_` prefix; there are no aliases.

All durations use Go duration strings, such as `250ms`, `1.5s`, and `2h`, in
YAML, environment variables, and flags. Booleans accept `true`/`false`.

## Listeners and credentials

| Environment variable | Flag | YAML key | Default |
| --- | --- | --- | --- |
| `MAILARKY_SMTP_PORT` | — | — | `1025` |
| `MAILARKY_IMAP_PORT` | — | — | `1993` |
| `MAILARKY_HTTP_PORT` | — | — | `8026` |
| `MAILARKY_SMTP_BIND_ADDR` | `--smtp` | `smtp` | `:1025` |
| `MAILARKY_IMAP_BIND_ADDR` | `--imap` | `imap` | `:1993` |
| `MAILARKY_HTTP_BIND_ADDR` | `--http` | `http` | `:8026` |
| `MAILARKY_USERNAME` | `--username` | `username` | `user@example.test` |
| `MAILARKY_PASSWORD` | `--password` | `password` | `local-imap-only` |
| `MAILARKY_SMTP_FOLDER` | `--smtp-folder` | `smtp_folder` | `Sent` |
| `MAILARKY_SMTP_REQUIRE_AUTH` | `--smtp-require-auth` | `smtp_require_auth` | `false` |
| `MAILARKY_SMTP_AUTH_ALLOW_INSECURE` | `--smtp-auth-allow-insecure` | `smtp_allow_insecure_auth` | `false` |
| `MAILARKY_HTTP_AUTH` | `--http-auth` | `http_auth` | empty |
| `MAILARKY_HTTP_AUTH_FILE` | `--http-auth-file` | `http_auth_file` | empty |
| `MAILARKY_WEBROOT` | `--webroot` | `webroot` | empty |

Port variables set `:PORT`; full bind-address variables override them. In
Compose, port variables control the published host ports and are not passed
through to container listeners. `MAILARKY_WEBMAIL_PORT` is Compose-only,
defaulting to 8027 for Roundcube.

Username/password configure the default account for both SMTP and IMAP.
Runtime accounts receive credentials through the HTTP API. SMTP AUTH always
uses account credentials; `smtp-require-auth` controls whether anonymous SMTP
is accepted. AUTH on plaintext requires explicitly allowing insecure AUTH.

HTTP credentials use whitespace-separated `username:password` entries, or
one entry per line in a file. Passwords can be plain text or bcrypt hashes.
The default HTTP API is unauthenticated. `webroot`, when set, must be an absolute
URL path, such as `/mail`; it prefixes every route and returned `api_base` once.

## TLS

| Environment variable | Flag | YAML key | Default |
| --- | --- | --- | --- |
| `MAILARKY_IMAP_CERT` | `--imap-tls-cert` | `imap_cert` | `/certs/server.crt` |
| `MAILARKY_IMAP_KEY` | `--imap-tls-key` | `imap_key` | `/certs/server.key` |
| `MAILARKY_SMTP_TLS_MODE` | `--smtp-tls-mode` | `smtp_tls_mode` | empty |
| `MAILARKY_SMTP_TLS_CERT` | `--smtp-tls-cert` | `smtp_cert` | empty |
| `MAILARKY_SMTP_TLS_KEY` | `--smtp-tls-key` | `smtp_key` | empty |
| `MAILARKY_SMTP_REQUIRE_STARTTLS` | `--smtp-require-starttls` | `smtp_require_starttls` | `false` |
| `MAILARKY_HTTP_TLS_CERT` | `--http-tls-cert` | `http_cert` | empty |
| `MAILARKY_HTTP_TLS_KEY` | `--http-tls-key` | `http_key` | empty |

IMAP always uses implicit TLS. SMTP mode is empty (plaintext), `starttls`, or
`tls` (implicit TLS). `smtp-require-starttls` requires mode `starttls` and rejects
MAIL until TLS is negotiated. SMTP reuses the IMAP certificate/key when no
SMTP-specific pair is given. HTTP uses TLS when its certificate/key pair is set.
The Compose file enables SMTP `starttls`; the standalone binary defaults to
plaintext SMTP. Minimum TLS version is 1.2 for SMTP and IMAP.

The `/certs` defaults refer to files bundled in the Docker image. For a
standalone binary, supply certificate paths explicitly; see
[run without Docker](../how-to/run-and-test.md#run-without-docker) to generate
a local test certificate and configure client trust.

## Storage and notifications

| Environment variable | Flag | YAML key | Default |
| --- | --- | --- | --- |
| `MAILARKY_DATABASE` | `--database` | `database` | empty, in memory |
| `MAILARKY_MAX_MESSAGES` | `--max-messages` | `max_messages` | `0`, unlimited |
| `MAILARKY_MAX_AGE` | `--max-age` | `max_age` | `0s`, unlimited |
| `MAILARKY_MAX_MESSAGE_SIZE` | `--max-message-size` | `max_message_bytes` | 50 MiB / 52428800 bytes |
| `MAILARKY_IGNORE_DUPLICATE_IDS` | `--ignore-duplicate-ids` | `ignore_duplicate_ids` | `false` |
| `MAILARKY_DUMP_PATH` | `--dump-path` | `dump_path` | empty |
| `MAILARKY_WEBHOOK_URL` | `--webhook-url` | `webhook_url` | empty |
| `MAILARKY_WEBHOOK_DELAY` | `--webhook-delay` | `webhook_delay` | `0s` |
| `MAILARKY_WEBHOOK_INTERVAL` | `--webhook-interval` | `webhook_interval` | `1s` |
| `MAILARKY_LABEL` | `--label` | `label` | empty |
| `MAILARKY_ENABLE_METRICS` | `--metrics` | `enable_metrics` | `false` |

The size environment variable and flag use integer MiB; the YAML key uses
bytes. Retention applies independently to each account, across all folders.
Explicit limits can remove INBOX fixtures when Sent grows. Age cleanup runs
periodically and on append. Duplicate Message-ID suppression is opt-in and
spans the account. Fault rules are never persisted.

The default database path is used for account metadata and the default store;
additional stores live under `DATABASE.mailboxes/`. Dumps are `.eml` files,
with separate subdirectories for runtime accounts. A failed dump does not undo
a successful message commit.

Webhooks use one worker per account, a bounded queue, and up to three attempts.
`webhook-delay` and `webhook-interval` govern that account's delivery timing.
`label` supplies the `Mailarky-Label` header. Webhook or WebSocket delivery is
observability, not durable message delivery.

## Sendmail helper

`mailarky sendmail` accepts `-f`, `-t`, `-i`, `-oi`, `-S host:port`, and `-ca file`.
`MAILARKY_SENDMAIL_SMTP_ADDR` supplies the default host/port (`localhost:1025`).
When STARTTLS is offered, it verifies TLS using system roots plus
`MAILARKY_SENDMAIL_CA` (or `-ca`). Inside the image, the bundled test certificate
is trusted by default. This helper uses anonymous SMTP recipient routing. Use an application SMTP
client with account credentials when test isolation depends on AUTH.
