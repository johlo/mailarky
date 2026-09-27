# Mail Sandbox

An independent, headless test mail service. SMTP capture, TLS IMAP and the HTTP
API share each account's mailbox. Create independent accounts through the API.
It reimplements Mailpit's headless feature set using Go protocol libraries,
with message-scoped toxics for concurrent tests. There is no
Mailpit dependency, browser mail client, or POP3 server.

```sh
git clone https://github.com/johlo/mail-sandbox.git
cd mail-sandbox
docker compose up -d --build --wait
curl -fsS http://localhost:8026/api/v1/messages
```

| Interface | Default |
| --- | --- |
| SMTP capture | `localhost:1025`, no authentication |
| IMAP | `localhost:1993`, implicit TLS |
| HTTP API | `http://localhost:8026` |
| IMAP account | `clinic@example.test` / `local-imap-only` |
| Initial folders | `INBOX`, `Sent`, `Archive` |

Trust [the bundled test certificate](testdata/tls/server.crt) in IMAP clients.
Compose publishes all three ports on loopback. SMTP deliveries appear in `Sent`;
fixtures default to `INBOX`. Storage is in memory unless a database is configured.
Outbound delivery is disabled unless relay/forwarding is explicitly configured.

The HTTP API supports inspection, raw MIME, attachments/thumbnails, search, read
flags, tags, deletion, JSON sending, webhooks, WebSocket events, diagnostics,
retention, persistence, authentication/TLS and metrics. Review the
[compatibility and differences](docs/reference/compatibility.md) before replacing
an existing Mailpit deployment.

[Documentation](docs/README.md) follows Diátaxis:

| Need | Guide |
| --- | --- |
| Learn the shared mailbox | [First mailbox tutorial](docs/tutorials/first-mailbox.md) |
| Create a mailbox per test | [Separate mailboxes](docs/how-to/separate-mailboxes.md) |
| Exercise failures in concurrent tests | [Use message-scoped toxics](docs/how-to/toxics.md) |
| Configure an application | [Integration](docs/how-to/integrate-with-application.md) |
| Seed historical or custom MIME mail | [Test scenarios](docs/how-to/seed-test-scenarios.md) |
| Run locally, persist mail, or use CI | [Run and test](docs/how-to/run-and-test.md) |
| Look up the contract | [HTTP API](docs/reference/http-api.md), [OpenAPI](openapi.yaml), [configuration](docs/reference/configuration.md), [IMAP](docs/reference/imap-behavior.md) |
| Understand storage and isolation | [Design](docs/explanation/design.md) |
| Diagnose a problem | [Troubleshooting](docs/how-to/troubleshoot.md) |

Run `make test` for protocol/API tests with the Go race detector and `go vet`.
The HTML compatibility checker uses [Can I Email](https://www.caniemail.com/)
data under its [MIT license](internal/sandbox/data/LICENSE.caniemail).

Source layout:

| Path | Purpose |
| --- | --- |
| `cmd/mail-sandbox/` | Executable entry point and process signal handling |
| `internal/sandbox/` | Configuration, mailbox storage, protocols, HTTP API, toxics and their tests |
| `internal/sandbox/data/` | Embedded HTML compatibility data and its license |
| `testdata/tls/` | Shared TLS fixtures for Go tests, Docker and client examples |
| `scripts/` | Container smoke tests |
| `docs/` | Diátaxis documentation |

Build with `go build -o mail-sandbox ./cmd/mail-sandbox`, or start locally with
`make run`.
