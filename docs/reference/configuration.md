# Configuration reference

Load defaults, then `MAIL_EMULATOR_CONFIG` YAML, then environment variables, then
command-line flags. Unknown YAML fields and CLI flags fail startup. Flags use
Go syntax (`--flag=value` for booleans). The YAML keys below are the supported
configuration contract; the MP-prefixed environment variables ease migration.

## Defaults

SMTP `:1025`, TLS IMAP `:1993`, HTTP `:8026`. All process listeners bind to all
interfaces; Compose publishes them only on loopback. IMAP account:
`clinic@example.test` / `local-imap-only`. Initial folders: INBOX, Sent, Archive
and the configured SMTP folder if different;
SMTP captures in Sent. Auth is disabled on SMTP/HTTP. Outbound delivery is disabled.
Message storage is in memory; retain at most 500 messages, no age limit, 50 MiB
maximum raw message size. Webhook minimum interval is 1 second.

## Settings

| YAML key | CLI flag | Environment |
| --- | --- | --- |
| `smtp` | `--smtp` | `MP_SMTP_BIND_ADDR` |
| `imap` | `--imap` | `IMAP_EMULATOR_BIND_ADDR` |
| `http` | `--listen` | `MP_UI_BIND_ADDR` |
| `cert` | `--imap-tls-cert` | `IMAP_EMULATOR_CERT` |
| `key` | `--imap-tls-key` | `IMAP_EMULATOR_KEY` |
| `imap_username` | `--imap-username` | `IMAP_EMULATOR_USERNAME` |
| `imap_password` | `--imap-password` | `IMAP_EMULATOR_PASSWORD` |
| `smtp_folder` | `--smtp-folder` | `SMTP_EMULATOR_FOLDER` |
| `smtp_tls` | `--smtp-tls-mode` | `SMTP_EMULATOR_TLS_MODE` |
| `smtp_cert` | `--smtp-tls-cert` | `MP_SMTP_TLS_CERT` |
| `smtp_key` | `--smtp-tls-key` | `MP_SMTP_TLS_KEY` |
| `require_tls` | `--smtp-require-starttls` | `MP_SMTP_REQUIRE_STARTTLS` |
| `smtp_require_tls` | `--smtp-require-tls` | `MP_SMTP_REQUIRE_TLS` |
| `smtp_auth_file` | `--smtp-auth-file` | `MP_SMTP_AUTH_FILE` |
| `smtp_accept_any` | `--smtp-auth-accept-any` | `MP_SMTP_AUTH_ACCEPT_ANY` |
| `smtp_allow_insecure_auth` | `--smtp-auth-allow-insecure` | `MP_SMTP_AUTH_ALLOW_INSECURE` |
| `http_auth_file` | `--ui-auth-file` | `MP_UI_AUTH_FILE` |
| `http_cert` | `--ui-tls-cert` | `MP_UI_TLS_CERT` |
| `http_key` | `--ui-tls-key` | `MP_UI_TLS_KEY` |
| `send_auth_file` | `--send-api-auth-file` | `MP_SEND_API_AUTH_FILE` |
| `send_accept_any` | `--send-api-auth-accept-any` | `MP_SEND_API_AUTH_ACCEPT_ANY` |
| `database` | `--database` | `MP_DATABASE` |
| `max_messages` | `--max` | `MP_MAX_MESSAGES` |
| `ignore_duplicate_ids` | `--ignore-duplicate-ids` | `MP_IGNORE_DUPLICATE_IDS` |
| `enable_chaos` | `--enable-chaos` | `MP_ENABLE_CHAOS` |
| `chaos_triggers` | `--chaos-triggers` | `MP_CHAOS_TRIGGERS` |
| `webhook_url` | `--webhook-url` | `MP_WEBHOOK_URL` |
| `webhook_delay` | `--webhook-delay` | `MP_WEBHOOK_DELAY` |
| `webhook_interval` | `--webhook-limit` | `MP_WEBHOOK_LIMIT` |
| `label` | `--label` | `MP_LABEL` |
| `tags_disable` | `--tags-disable` | `MP_TAGS_DISABLE` |
| `tags_title_case` | `--tags-title-case` | `MP_TAGS_TITLE_CASE` |
| `tags_username` | `--tags-username` | `MP_TAGS_USERNAME` |
| `tags_config` | `--tags-config` | `MP_TAGS_CONFIG` |
| `tag` | `--tag` | `MP_TAG` |
| `relay_all` | `--smtp-relay-all` | `MP_SMTP_RELAY_ALL` |
| `relay_matching` | `--smtp-relay-matching` | `MP_SMTP_RELAY_MATCHING` |
| `spamassassin` | `--spamassassin` | `MP_SPAMASSASSIN` |
| `allow_internal_http_requests` | `--allow-internal-http-requests` | `MP_ALLOW_INTERNAL_HTTP_REQUESTS` |
| `block_remote_css_and_fonts` | `--block-remote-css-and-fonts` | `MP_BLOCK_REMOTE_CSS_AND_FONTS` |
| `enable_prometheus` | `--enable-prometheus` | `MP_ENABLE_PROMETHEUS` |
| `webroot` | `--webroot` | `MP_WEBROOT` |
| `api_cors` | `--api-cors` | `MP_API_CORS` |
| `allowed_hosts` | `--allowed-hosts` | `MP_ALLOWED_HOSTS` |
| `dump_path` | `--dump-path` | `MP_DUMP_PATH` |
| `max_age` | `--max-age` | `MP_MAX_AGE` |
| `max_message_bytes` | `--max-message-size` | `MP_MAX_MESSAGE_SIZE` |
| `smtp_auth` | — | `MP_SMTP_AUTH` |
| `http_auth` | — | `MP_UI_AUTH` |
| `send_auth` | — | `MP_SEND_API_AUTH` |

`SMTP_EMULATOR_PORT`, `IMAP_EMULATOR_PORT`, and `IMAP_EMULATOR_HTTP_PORT` also
set process listener ports; explicit bind-address variables/flags take precedence.
In the supplied Compose file these variables override **host publication** only.
Set service `environment` entries or mount a YAML config for container settings.

`max_message_bytes` is bytes in YAML; `--max-message-size` and
`MP_MAX_MESSAGE_SIZE` use MiB. `max_messages: 0` is unlimited. Duration flags/YAML
use Go duration strings (`500ms`, `5m`, `24h`); MP_WEBHOOK_DELAY and
MP_WEBHOOK_LIMIT use integer seconds. MP_MAX_AGE uses a duration string.

`enable_chaos`/`chaos_triggers` and their MP aliases are recognized only to reject
global failures. Use [message-scoped toxics](../how-to/toxics.md).

## TLS and authentication

IMAP always uses implicit TLS with the configured cert/key; the container bundles
public test fixtures in `/certs`. `make run` uses `testdata/tls` instead.
Certificates contain localhost, 127.0.0.1 and imap-emulator names.

For SMTP, supplying an SMTP cert/key enables optional STARTTLS. Require it with
`--smtp-require-starttls` / MP_SMTP_REQUIRE_STARTTLS. For implicit TLS use
`--smtp-require-tls` / MP_SMTP_REQUIRE_TLS, or `smtp_tls: tls`. The two required
modes are mutually exclusive. Explicit `smtp_tls: starttls`/`tls` can reuse the
IMAP certificate. HTTP TLS requires both http_cert and http_key.

Credential files contain `username:password` lines, blank/comment lines ignored.
Passwords can be plaintext test credentials or bcrypt hashes. Inline MP_*_AUTH
variables contain space-separated username:password pairs and are not available
as CLI flags. SMTP accepts PLAIN and LOGIN. Authentication over plaintext SMTP
requires the explicit allow-insecure option. SMTP accept-any allows arbitrary
credentials or no AUTH; otherwise configured SMTP credentials are required.

HTTP uses Basic auth when configured. Send credentials override general HTTP
auth for `/api/v1/send`; send-api-auth-accept-any accepts any supplied Basic
credentials. Health probes bypass auth. Create independent mailbox accounts through `/api/v1/mailboxes`. Their IMAP
credentials are separate from server-wide SMTP and HTTP authentication; scoped
HTTP access still uses the configured service credentials.

## Relay and forwarding

Set nested YAML `relay` / `forward` objects, or MP_SMTP_RELAY_CONFIG /
MP_SMTP_FORWARD_CONFIG to separate YAML files. Each supports:

```yaml
host: localhost
port: 2525
auth: none                  # none, plain, login, cram-md5
username: ''
password: ''
secret: ''                  # CRAM-MD5 secret
starttls: false
tls: false
allow-insecure: false       # explicitly skip outbound TLS verification
return-path: ''
override-from: ''
allowed-recipients: ''      # regex, manual release
blocked-recipients: ''      # regex, any delivery
preserve-message-ids: false # manual release normally generates a fresh ID
forward-smtp-errors: false
# Forward config additionally uses:
to: copy@example.test       # comma-separated envelope recipients
```

Each field also has an MP_SMTP_RELAY_* / MP_SMTP_FORWARD_* environment alias
using uppercase and underscores. `forward-smtp-errors` uses `FWD_SMTP_ERRORS`.
Port defaults to 25. STARTTLS and implicit TLS are mutually exclusive.
Automatic relay requires relay_all or relay_matching (mutually exclusive).
Forwarding requires a host and `to`. Captured originals remain unchanged. Do not
configure a relay/forward destination that loops back into this same service.

## Tagging and diagnostics

X-Tags and plus-address tags are enabled by default. Disable either using
`tags_disable: x-tags,plus-addresses`. `tags_config` points to YAML with
`filters: [{match: 'to:patient@example.test', tags: 'Patient, Test'}]`.
Inline `filters` is also supported. `--tag` / MP_TAG uses shell-like quoted
`tag=match` expressions; no shell expansion is performed. Username/title casing
and API tag mutation are independent options.

Link checking and remote CSS requests block private/loopback addresses by default,
including DNS results and redirects. Explicitly enable allow_internal_http_requests
for local fixtures. block_remote_css_and_fonts disables external stylesheet loads.
The HTML checker loads at most ten external link stylesheets; nested CSS imports
and fonts are not downloaded. SpamAssassin needs a spamd host:port.

`dump_path` writes best-effort .eml copies after capture; failures are logged and
never cause a committed message to be rejected. `webhook_url` posts message
summaries, using Basic auth from URL credentials if supplied. Delay and interval
apply to the bounded in-memory webhook worker, with three attempts per message.
`label` is included in the Mailpit-Label webhook header.

`webroot` prefixes API routes, `api_cors` accepts comma-separated origins,
`allowed_hosts` accepts comma-separated hostnames. Prometheus metrics are exposed
at /metrics only when enabled. The Docker health probe assumes the default
HTTP path/port; override it when changing webroot or enabling HTTPS.

## Provisioned accounts

The default account remains configurable above. Runtime accounts are created via
`POST /api/v1/mailboxes` and inherit listener, auth, retention and outbound delivery
settings. Each has a separate store, folders, UID space, toxic registry and queues.
Persistent account metadata lives in MP_DATABASE; data files live in the sibling
`MP_DATABASE.mailboxes/` directory. See [separate mailboxes](../how-to/separate-mailboxes.md).
