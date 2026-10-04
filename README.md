# Mailarky

Mailarky tests **SMTP sending and IMAP reading together**, with **an isolated account per test** and **programmable protocol failures**.

Your application uses its normal email clients. Your tests create accounts,
seed incoming history, inspect outgoing mail without changing its read state,
and inject authentication, command, or message failures through HTTP. SMTP,
IMAP, and HTTP share the same raw messages, folders, flags, and UIDs.

Use it for reply import, mailbox synchronization, retries, and parallel test
suites. Each account's credentials work for **both SMTP and IMAP**: two tests
can send to the same `alice@customer.test` address without sharing mail.

Mailarky's protocol implementations are built on
[emersion/go-imap v2](https://github.com/emersion/go-imap/tree/v2) for IMAP and
[emersion/go-smtp](https://github.com/emersion/go-smtp) for SMTP. Mailarky uses
[IMAP](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks) and
[SMTP](https://github.com/johlo/go-smtp/tree/smtp-protocol-hooks) forks that add
the hooks needed for protocol fault injection.

## Quick start

```sh
git clone https://github.com/johlo/mailarky.git
cd mailarky
docker compose --profile webmail up -d --build --wait
```

| Interface | Default address |
| --- | --- |
| SMTP | `localhost:1025`, with STARTTLS in Compose |
| IMAP | `localhost:1993`, implicit TLS |
| HTTP control API | `http://localhost:8026/api/v1` |
| Optional Roundcube webmail | <http://localhost:8027> |

Trust [`testdata/tls/server.crt`](testdata/tls/server.crt) in your test clients.
Log into Roundcube with `user@example.test` / `local-imap-only`, or a test
account's generated credentials. Omit `--profile webmail` to run only Mailarky.

All published ports bind to loopback. Set `MAILARKY_SMTP_PORT`,
`MAILARKY_IMAP_PORT`, `MAILARKY_HTTP_PORT`, and `MAILARKY_WEBMAIL_PORT` to change
them. See [configuration](docs/reference/configuration.md) for listener settings.

## An account per test

```sh
# Create an account; keep its id, username, password and api_base.
curl -fsS http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{}'

# Seed history in that account's INBOX.
curl -fsS http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages \
  -H 'Content-Type: application/json' \
  -d '{"from":"alice@customer.test","to":["app@example.test"],"subject":"Earlier reply","date":"2020-01-02T03:04:05Z","body":"Hello"}'

# Configure the application with the returned credentials for SMTP and IMAP.
# After it sends mail, inspect the account without marking anything as read.
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/messages?query=folder:Sent'

# Clean up after the test.
curl -fsS -X DELETE http://localhost:8026/api/v1/accounts/ACCOUNT_ID
```

Authenticated SMTP stores one copy in the authenticated account's `Sent`
folder, regardless of the recipient addresses. Anonymous SMTP routes by the
account's registered recipient addresses, delivering one copy per account in
a multi-recipient transaction. Unclaimed addresses go to `default`.

The [first-account tutorial](docs/tutorials/first-mailbox.md) includes working
SMTP, HTTP, and IMAP clients.

## Programmable failures

Every fault has a **trigger**, optional **filter**, and typed **action**:

```json
{
  "name": "temporary-submit-error",
  "trigger": {"protocol": "smtp", "command": "DATA", "phase": "content"},
  "filter": {"message": {"headers": {"X-Test-ID": "retry-case"}}},
  "action": {"type": "reject", "code": 451, "enhanced_code": "4.3.0"},
  "max_hits": 1
}
```

POST this to `/api/v1/accounts/ACCOUNT_ID/faults`. Server-wide faults use a
separate registry at `/api/v1/faults`. Rules support delays, disconnects,
rejections, malformed replies, capability changes, lost acknowledgements,
UIDVALIDITY resets, and selected IMAP content changes. Inspect hit counts,
disable rules, or bound them by probability, expiry, and maximum hits.
See [inject failures](docs/how-to/fault-injection.md).

For **latency, bandwidth limits, TCP resets, and connection outages**, combine
Mailarky with [Toxiproxy](https://github.com/Shopify/toxiproxy). Keep the HTTP
control API directly accessible while SMTP or IMAP connections are failing.

## Focus and boundaries

- Raw MIME is the source of truth. HTTP inspection has no read-state side effects;
  IMAP BODY fetches set `\Seen`, while BODY.PEEK and EXAMINE do not.
- Selected IMAP sessions track their own sequence numbers. External changes
  produce EXISTS, EXPUNGE, and flag updates at safe command boundaries and during IDLE.
- Storage is in memory by default. Optional bbolt persistence preserves accounts,
  credentials, messages, and UID identity. Fault rules are ephemeral.
- Retention is unlimited by default. Optional limits apply independently to each
  account, across its folders.
- Mail is captured locally. There is no relay, forwarding, POP3, or built-in webmail UI.
  Roundcube provides browsing over the real mail protocols.
- SMTP has one final result per transaction. A content fault rejects a fan-out
  transaction before storing copies; an after fault runs after storage. Commits
  across separate account databases are not atomic if a persistence write fails.
- This is a single-process test service. The HTTP control API administers all accounts;
  it is not a tenancy boundary for mutually untrusted users.

## Documentation

| Task | Guide |
| --- | --- |
| Connect an application | [Integration](docs/how-to/integrate-with-application.md) |
| Isolate parallel tests | [Accounts](docs/how-to/separate-mailboxes.md) |
| Seed history or raw MIME | [Fixtures](docs/how-to/seed-test-scenarios.md) |
| Run, persist, or browse mail | [Run and test](docs/how-to/run-and-test.md) |
| Look up API and settings | [HTTP API](docs/reference/http-api.md), [OpenAPI](openapi.yaml), [configuration](docs/reference/configuration.md) |
| Understand protocol behavior | [IMAP](docs/reference/imap-behavior.md), [capabilities](docs/reference/capabilities.md) |
| Understand the implementation | [Architecture](docs/explanation/architecture.md), [design](docs/explanation/design.md) |
| Diagnose a problem | [Troubleshooting](docs/how-to/troubleshoot.md) |

## Development

`make test` runs Go tests with the race detector and then `go vet`. Tests cover
real SMTP/IMAP sockets, account isolation, persistence, API behavior, fault
recovery, IMAP updates, and concurrent mutation. `scripts/smoke.py` exercises the
Docker service using Python's independent SMTP and IMAP clients.

`make run` uses the bundled certificate. Build with
`go build -o mailarky ./cmd/mailarky` or install a published revision using
`go install github.com/johlo/mailarky/cmd/mailarky@REVISION`.

Mailarky imports the public [SMTP](https://github.com/johlo/go-smtp) and
[IMAP](https://github.com/johlo/go-imap) forks directly under their own module
paths. `go.mod` pins exact revisions and contains no `replace` directives.
Each fork retains its upstream license and tests and runs its own CI.

The IMAP implementation uses [go-imap v2](https://github.com/johlo/go-imap/tree/imap-v2-protocol-hooks)
with a pinned revision. V2 is still in development upstream; Mailarky keeps
independent protocol tests around its session backend and fault hooks. The
listener advertises IMAP4rev1; the library version does not enable IMAP4rev2
protocol features automatically.
