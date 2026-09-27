# Mail Sandbox

A disposable mail server for automated tests. Your application sends mail over
SMTP and reads a mailbox over IMAP, and your tests seed, inspect and break that
mail through an HTTP API. All three reach the **same stored messages**, so a
test can cover a complete email round trip with no real mail provider.

## When to use it

Use Mail Sandbox when the code under test does more than send email:

- **It reads a mailbox.** Examples are importing replies, syncing a
  correspondence history or processing incoming requests. Without a sandbox,
  this code normally needs a real mail account.
- **It needs mail history to exist first.** You can seed messages with
  historical dates, custom headers or raw MIME, then let the application import
  them through its normal IMAP client.
- **Tests run in parallel.** Each test can create its own mailbox and remove it
  afterwards, so tests never share state or clean up each other's mail.
- **You need to test failures.** SMTP rejections, slow deliveries and IMAP
  anomalies can target a single message, so one test's failure never affects
  another test's mail.

## Key features

| Feature | What it gives you |
| --- | --- |
| One mailbox, three interfaces | SMTP deliveries land in `Sent`, seeded fixtures in `INBOX`. HTTP and TLS IMAP see the same message, flags and raw MIME. |
| Mailbox per test | `POST /api/v1/mailboxes` creates an isolated account with its own IMAP credentials, SMTP recipient addresses, folders, UIDs and toxics. Delete it when the test ends. |
| Message-scoped fault injection ("toxics") | Reject or delay SMTP, and delay, hide or rewrite messages returned over IMAP. Each toxic matches specific messages, never a whole server. |
| HTTP inspection API | Search with a filter syntax (`to:`, `subject:`, `message-id:`, …), then read message details, raw MIME, attachments, tags and read flags. |
| Test data seeding | Add JSON fixtures, import raw `.eml` files or use IMAP `APPEND`, with any `Date` and `Message-ID`. Reusing a Message-ID in two folders tests deduplication. |
| Realistic IMAP | Implicit TLS with a bundled test certificate, stable UIDs, and a UIDVALIDITY that changes when in-memory state is lost on restart. It exercises the same client code as production. |
| Safe by default | Mail never leaves the sandbox: there is no relay or forwarding. Storage is in memory unless you turn on persistence (bbolt). |

Other features are WebSocket and webhook notifications, retention limits,
HTML compatibility and link checks, Prometheus metrics, and optional SMTP/HTTP
authentication and TLS. See [capabilities](docs/reference/capabilities.md)
for the full list.

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/architecture/context-dark.svg">
  <img alt="System context: the test suite and the application under test use Mail Sandbox; optional webhook receiver and SpamAssassin" src="docs/architecture/context.svg">
</picture>

The [architecture page](docs/explanation/architecture.md) zooms in on the
containers and components, following the C4 model.

## Quick start

```sh
git clone https://github.com/johlo/mail-sandbox.git
cd mail-sandbox
docker compose up -d --build --wait
```

| Interface | Address | Notes |
| --- | --- | --- |
| SMTP | `localhost:1025` | No authentication |
| IMAP | `localhost:1993` | Implicit TLS; trust [`testdata/tls/server.crt`](testdata/tls/server.crt) |
| HTTP API | `http://localhost:8026` | [OpenAPI](openapi.yaml) |
| Default account | `clinic@example.test` / `local-imap-only` | Folders `INBOX`, `Sent`, `Archive` |

Ports bind to loopback only. Change them with `MAIL_SANDBOX_SMTP_PORT`,
`MAIL_SANDBOX_IMAP_PORT` and `MAIL_SANDBOX_HTTP_PORT`.

A typical test proceeds in four steps:

```sh
# 1. Create a mailbox that belongs only to this test
curl -fsS http://localhost:8026/api/v1/mailboxes -H 'Content-Type: application/json' -d '{}'
#    → {"id": "<uuid>", "username": "mailbox-<uuid>", "password": ..., "recipients": ["<uuid>@mailbox.test"], "api_base": "/mailboxes/<uuid>", ...}

# 2. Seed incoming history for the app to import over IMAP
curl -fsS http://localhost:8026/mailboxes/<uuid>/messages -H 'Content-Type: application/json' \
  -d '{"from":"alice@example.test","to":["<uuid>@mailbox.test"],"subject":"Earlier reply","date":"2020-01-02T03:04:05Z"}'

# 3. Let the app send to <uuid>@mailbox.test over SMTP, then assert on what arrived
curl -fsS 'http://localhost:8026/mailboxes/<uuid>/api/v1/search?query=subject:"Welcome"'

# 4. Clean up
curl -fsS -X DELETE http://localhost:8026/api/v1/mailboxes/<uuid>
```

The [first mailbox tutorial](docs/tutorials/first-mailbox.md) runs one message
through SMTP, HTTP and IMAP step by step.

## Limitations

This is a test double, not a mail server. It runs as one process and supports
no POP3, no browser UI and no IMAP IDLE (clients must poll). A body FETCH does
not set `\Seen` implicitly. Toxics are always lost on restart; messages and
runtime accounts are lost too unless persistence is configured. The HTTP API is not a security boundary
between untrusted tenants.

## Documentation

| Need | Guide |
| --- | --- |
| Connect your application | [Integration](docs/how-to/integrate-with-application.md) |
| Create a mailbox per test | [Separate mailboxes](docs/how-to/separate-mailboxes.md) |
| Simulate failures | [Message-scoped toxics](docs/how-to/toxics.md) |
| Seed historical or custom MIME mail | [Test scenarios](docs/how-to/seed-test-scenarios.md) |
| Run locally, persist mail, use in CI | [Run and test](docs/how-to/run-and-test.md) |
| Look up the contract | [HTTP API](docs/reference/http-api.md), [OpenAPI](openapi.yaml), [configuration](docs/reference/configuration.md), [IMAP behavior](docs/reference/imap-behavior.md) |
| See every capability | [Capabilities](docs/reference/capabilities.md) |
| See how it is built | [Architecture](docs/explanation/architecture.md), [design](docs/explanation/design.md) |
| Diagnose a problem | [Troubleshooting](docs/how-to/troubleshoot.md) |

## Development

Requires the Go version in `go.mod`. `make test` runs the protocol and API tests
with the race detector, then `go vet`. `make run` starts the service locally with
the bundled certificate. `go build -o mail-sandbox ./cmd/mail-sandbox` builds the
binary.

The entry point is `cmd/mail-sandbox/`. Everything else, including the tests,
lives in `internal/sandbox/`. The container smoke test is in `scripts/`.

The HTML compatibility checker uses [Can I Email](https://www.caniemail.com/)
data under its [MIT license](internal/sandbox/data/LICENSE.caniemail).
