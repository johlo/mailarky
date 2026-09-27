# IMAP emulator

A local TLS IMAP server for integration and end-to-end tests. Seed synthetic
emails through a small HTTP API, then let your application fetch them using
its normal IMAP client. Built with `github.com/emersion/go-imap` and an
in-memory mailbox backend; no database or SMTP server is required.

## Run with Docker

```sh
git clone https://github.com/johlo/imap-emulator.git
cd imap-emulator
docker compose up -d --build
```

| Setting | Default |
| --- | --- |
| TLS IMAP | `localhost:1993` |
| HTTP fixture API | `http://localhost:8026` |
| Username | `clinic@example.test` |
| Password | `local-imap-only` |
| Folders | `INBOX`, `Sent`, `Archive` |

Both published ports bind to host loopback. Set `IMAP_EMULATOR_PORT` and
`IMAP_EMULATOR_HTTP_PORT` to choose different host ports when using Compose.
The image is built locally; it does not require a container registry login.

## Seed messages

```sh
curl -fsS http://localhost:8026/messages \
  -H 'Content-Type: application/json' \
  -d '{"folder":"INBOX","from":"alice@example.test","to":["clinic@example.test"],"subject":"Historical reply","date":"2020-01-02T03:04:05Z"}'
```

The response contains `folder` and `message_id`. Missing dates default to now;
missing Message-IDs are generated. Supply the same `message_id` in two folders
to exercise an application's deduplication logic. The API also accepts `cc`,
`bcc`, `internal_date`, `flags` and `body`.

For outgoing mail, set `folder` to `Sent`, `from` to the sender, and `to` to
the recipient. This stores a fixture; no message is sent to any recipient.

Full request and response definitions: [openapi.yaml](openapi.yaml).
`GET /healthz` returns HTTP 200 when the HTTP service is running.

## Connect your application

Use implicit TLS on port 1993, with the test credentials above. Trust
[`testdata/tls/server.crt`](testdata/tls/server.crt) in your IMAP client's root
certificate pool. Its names cover `imap-emulator`, `localhost` and `127.0.0.1`.
The bundled certificate and private key are public test fixtures.

For a Linux Go application in Compose, mount the certificate into its system
trust directory and connect to the service name:

```yaml
services:
  app:
    volumes:
      - ./imap-emulator/testdata/tls/server.crt:/etc/ssl/certs/imap-emulator.pem:ro
  imap-emulator:
    build: ./imap-emulator
```

## Behavior and limits

- Mail is stored in memory. Restarting clears it and changes UIDVALIDITY.
- Reading, searching and appending messages are supported. Folder changes,
  flag edits, copying via IMAP and deletions are unsupported.
- Existing message UIDs remain stable while the process is running.
- HTTP fixture writes and IMAP reads can run concurrently.
- The service does not receive SMTP or send external mail. Use a separate
  SMTP capture service if your tests also exercise email delivery.
- The fixture API is for local tests, uses no authentication, and accepts at
  most 1 MiB of JSON per request. Use unique addresses and Message-IDs for
  parallel tests; there is no global reset endpoint.

## Develop and test

Requires the Go version specified in `go.mod`.

```sh
make test  # TLS round trips, concurrent reads/writes and malformed fixtures
make run   # uses the bundled certificate; listens on ports 1993 and 8026
```

Process environment settings:

| Variable | Default |
| --- | --- |
| `IMAP_EMULATOR_PORT` | `1993` |
| `IMAP_EMULATOR_HTTP_PORT` | `8026` |
| `IMAP_EMULATOR_CERT` | `/certs/server.crt` |
| `IMAP_EMULATOR_KEY` | `/certs/server.key` |

Unlike Compose's host-port overrides, process variables change the ports the
binary listens on. Custom certificates can be mounted at the default paths or
selected through the environment settings.
