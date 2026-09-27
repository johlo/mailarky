# Configuration

The defaults suit most test stacks, so you usually need nothing here.

Settings are applied in this order, later ones winning: defaults, the YAML file
named by `MAIL_SANDBOX_CONFIG`, environment variables, command-line flags.
Unknown YAML keys and flags stop startup.

## Defaults

| | |
| --- | --- |
| Ports | SMTP 1025, IMAP 1993 (TLS), HTTP 8026 |
| Default account | `clinic@example.test` / `local-imap-only`, folders `INBOX`, `Sent`, `Archive` |
| SMTP deliveries | Stored in `Sent` |
| Authentication | None on SMTP or HTTP |
| Storage | In memory, at most 500 messages, 50 MiB per message |
| Outbound mail | Disabled |

## Settings

Each row lists the YAML key, then the flag and environment variable.

### Listeners and accounts

| YAML | Flag / environment | Purpose |
| --- | --- | --- |
| `smtp` | `--smtp` / `MP_SMTP_BIND_ADDR` | SMTP listen address |
| `imap` | `--imap` / `MAIL_SANDBOX_IMAP_BIND_ADDR` | IMAP listen address |
| `http` | `--listen` / `MP_UI_BIND_ADDR` | HTTP listen address |
| `cert`, `key` | `--imap-tls-cert`, `--imap-tls-key` / `MAIL_SANDBOX_IMAP_CERT`, `_KEY` | IMAP certificate |
| `imap_username`, `imap_password` | `--imap-username`, `--imap-password` / `MAIL_SANDBOX_IMAP_USERNAME`, `_PASSWORD` | Default account login |
| `smtp_folder` | `--smtp-folder` / `MAIL_SANDBOX_SMTP_FOLDER` | Folder for SMTP deliveries |

`MAIL_SANDBOX_SMTP_PORT`, `_IMAP_PORT` and `_HTTP_PORT` set the process ports.
In `compose.yaml` they set only the published host ports.

### Storage

| YAML | Flag / environment | Purpose |
| --- | --- | --- |
| `database` | `--database` / `MP_DATABASE` | bbolt file; enables persistence |
| `max_messages` | `--max` / `MP_MAX_MESSAGES` | Per-account limit (500; `0` = unlimited) |
| `max_age` | `--max-age` / `MP_MAX_AGE` | Delete mail older than this (e.g. `24h`) |
| `max_message_bytes` | `--max-message-size` / `MP_MAX_MESSAGE_SIZE` | Size limit (bytes in YAML, MiB otherwise) |
| `ignore_duplicate_ids` | `--ignore-duplicate-ids` / `MP_IGNORE_DUPLICATE_IDS` | Drop repeated Message-IDs |
| `dump_path` | `--dump-path` / `MP_DUMP_PATH` | Also write each message as an `.eml` file |

### SMTP TLS and authentication

| YAML | Flag / environment | Purpose |
| --- | --- | --- |
| `smtp_cert`, `smtp_key` | `--smtp-tls-cert`, `--smtp-tls-key` / `MP_SMTP_TLS_CERT`, `_KEY` | Enables optional STARTTLS |
| `smtp_tls` | `--smtp-tls-mode` / `MAIL_SANDBOX_SMTP_TLS_MODE` | `starttls` or `tls` (may reuse the IMAP certificate) |
| `require_tls` | `--smtp-require-starttls` / `MP_SMTP_REQUIRE_STARTTLS` | Require STARTTLS |
| `smtp_require_tls` | `--smtp-require-tls` / `MP_SMTP_REQUIRE_TLS` | Implicit TLS |
| `smtp_auth_file` | `--smtp-auth-file` / `MP_SMTP_AUTH_FILE` | Credentials file |
| `smtp_auth` | `MP_SMTP_AUTH` | Inline credentials |
| `smtp_accept_any` | `--smtp-auth-accept-any` / `MP_SMTP_AUTH_ACCEPT_ANY` | Accept any credentials, or none |
| `smtp_allow_insecure_auth` | `--smtp-auth-allow-insecure` / `MP_SMTP_AUTH_ALLOW_INSECURE` | Allow AUTH without TLS |

### HTTP

| YAML | Flag / environment | Purpose |
| --- | --- | --- |
| `http_cert`, `http_key` | `--ui-tls-cert`, `--ui-tls-key` / `MP_UI_TLS_CERT`, `_KEY` | HTTPS |
| `http_auth_file`, `http_auth` | `--ui-auth-file` / `MP_UI_AUTH_FILE`, `MP_UI_AUTH` | Basic auth |
| `send_auth_file`, `send_auth` | `--send-api-auth-file` / `MP_SEND_API_AUTH_FILE`, `MP_SEND_API_AUTH` | Separate auth for `/api/v1/send` |
| `send_accept_any` | `--send-api-auth-accept-any` / `MP_SEND_API_AUTH_ACCEPT_ANY` | Accept any send credentials |
| `webroot` | `--webroot` / `MP_WEBROOT` | Path prefix for all routes |
| `api_cors` | `--api-cors` / `MP_API_CORS` | Allowed CORS origins |
| `allowed_hosts` | `--allowed-hosts` / `MP_ALLOWED_HOSTS` | Allowed `Host` headers |
| `enable_prometheus` | `--enable-prometheus` / `MP_ENABLE_PROMETHEUS` | Serve `/metrics` |

Health probes skip authentication. If you change `webroot` or enable HTTPS,
update the Docker health check too.

### Notifications, tags and checks

| YAML | Flag / environment | Purpose |
| --- | --- | --- |
| `webhook_url` | `--webhook-url` / `MP_WEBHOOK_URL` | POST a summary of each new message |
| `webhook_delay`, `webhook_interval` | `--webhook-delay`, `--webhook-limit` / `MP_WEBHOOK_DELAY`, `MP_WEBHOOK_LIMIT` | Delay and minimum interval (seconds in env) |
| `label` | `--label` / `MP_LABEL` | Sent as the `Mail-Sandbox-Label` webhook header |
| `tags_disable` | `--tags-disable` / `MP_TAGS_DISABLE` | `x-tags`, `plus-addresses` |
| `tags_config`, `tag` | `--tags-config`, `--tag` / `MP_TAGS_CONFIG`, `MP_TAG` | Automatic tagging rules |
| `tags_username`, `tags_title_case` | `--tags-username`, `--tags-title-case` / `MP_TAGS_USERNAME`, `MP_TAGS_TITLE_CASE` | Tag by SMTP username; title-case tags |
| `spamassassin` | `--spamassassin` / `MP_SPAMASSASSIN` | spamd `host:port` |
| `allow_internal_http_requests` | `--allow-internal-http-requests` / `MP_ALLOW_INTERNAL_HTTP_REQUESTS` | Let link/CSS checks reach private addresses |
| `block_remote_css_and_fonts` | `--block-remote-css-and-fonts` / `MP_BLOCK_REMOTE_CSS_AND_FONTS` | Don't fetch external stylesheets |

Durations in YAML and flags use Go syntax (`500ms`, `24h`). Boolean flags use
`--flag=value`.

## Credentials

Credential files hold one `username:password` per line, as plain text or a
bcrypt hash. Inline `MP_*_AUTH` variables take space-separated pairs. SMTP
supports PLAIN and LOGIN.

Accounts created through `/api/v1/mailboxes` have their own IMAP credentials.
They use the server-wide SMTP and HTTP settings above.

## Relay and forwarding

Off by default. **Relay** delivers captured mail onward, either when you call
`/release` or automatically (`relay_all` / `relay_matching`). **Forwarding**
copies every message to fixed addresses. Configure them with nested `relay` /
`forward` YAML, or a separate YAML file named by `MP_SMTP_RELAY_CONFIG` /
`MP_SMTP_FORWARD_CONFIG`:

```yaml
host: smtp.example.test
port: 25                    # default
auth: none                  # none, plain, login, cram-md5
username: ''
password: ''
secret: ''                  # CRAM-MD5 secret
starttls: false             # starttls and tls are mutually exclusive
tls: false
allow-insecure: false       # skip TLS verification
return-path: ''
override-from: ''
allowed-recipients: ''      # regex, for manual release
blocked-recipients: ''      # regex, always applied
preserve-message-ids: false
forward-smtp-errors: false  # return delivery failures to the SMTP client
to: copy@example.test       # forwarding only
```

Every key also has an `MP_SMTP_RELAY_*` / `MP_SMTP_FORWARD_*` variable
(uppercase, underscores). Don't point relay or forwarding back at the sandbox.

## Previous environment names

Names from the `imap-emulator` era still work. The new name wins if both are set.

| Old | New |
| --- | --- |
| `MAIL_EMULATOR_CONFIG` | `MAIL_SANDBOX_CONFIG` |
| `SMTP_EMULATOR_PORT`, `IMAP_EMULATOR_PORT`, `IMAP_EMULATOR_HTTP_PORT` | `MAIL_SANDBOX_SMTP_PORT`, `_IMAP_PORT`, `_HTTP_PORT` |
| `IMAP_EMULATOR_BIND_ADDR`, `_CERT`, `_KEY`, `_USERNAME`, `_PASSWORD` | `MAIL_SANDBOX_IMAP_*` equivalents |
| `SMTP_EMULATOR_FOLDER`, `SMTP_EMULATOR_TLS_MODE` | `MAIL_SANDBOX_SMTP_FOLDER`, `_SMTP_TLS_MODE` |
| `IMAP_EMULATOR_HTTP_URL` (smoke script) | `MAIL_SANDBOX_HTTP_URL` |

`enable_chaos` and `chaos_triggers` are accepted only to fail with an error.
Use [toxics](../how-to/toxics.md) instead.
