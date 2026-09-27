# IMAP emulator

A local TLS IMAP server for integration and end-to-end tests. Seed synthetic
emails through an HTTP API, then fetch them with your application's normal
IMAP client. Messages live in memory; no database or SMTP server is required.

## Documentation

Start with the [documentation index](docs/README.md), organized using
[Diátaxis](https://diataxis.fr/):

| Your goal | Read |
| --- | --- |
| Learn by running a complete example | [Your first test mailbox](docs/tutorials/first-mailbox.md) |
| Connect an application | [Integrate with your application](docs/how-to/integrate-with-application.md) |
| Create historical mail, sent mail, or duplicates | [Seed test scenarios](docs/how-to/seed-test-scenarios.md) |
| Run locally or in CI | [Run and test the emulator](docs/how-to/run-and-test.md) |
| Resolve a connection or fixture error | [Troubleshoot](docs/how-to/troubleshoot.md) |
| Look up settings and behavior | [Configuration](docs/reference/configuration.md), [HTTP API](docs/reference/http-api.md), [IMAP behavior](docs/reference/imap-behavior.md) |
| Understand the design | [How the emulator works](docs/explanation/design.md) |

## Quick start

Requires access to this repository, Git, Docker with Compose, and curl.

```sh
git clone https://github.com/johlo/imap-emulator.git
cd imap-emulator
docker compose up -d --build --wait --wait-timeout 60
curl -fsS http://localhost:8026/healthz
```

The health response is `ok`. Seed a message:

```sh
curl -fsS http://localhost:8026/messages \
  -H 'Content-Type: application/json' \
  -d '{"from":"alice@example.test","to":["clinic@example.test"],"subject":"Hello"}'
```

Connect over implicit TLS to `localhost:1993`, using `clinic@example.test` /
`local-imap-only`. Trust [the bundled test certificate](testdata/tls/server.crt)
in your client. The initial folders are `INBOX`, `Sent`, and `Archive`.

The server supports reading and appending mail. It does not receive SMTP or
send external mail. Restarting clears all messages. Both published ports bind
to loopback; the credentials and certificate are public test fixtures.

Stop it with `docker compose down`. For different ports, see
[run and test the emulator](docs/how-to/run-and-test.md#choose-host-ports).

## Development

Install the Go version specified in [go.mod](go.mod), then run `make test` for
race-checked tests and `go vet`. See the [development and CI guide](docs/how-to/run-and-test.md).
The machine-readable HTTP contract is [openapi.yaml](openapi.yaml).
