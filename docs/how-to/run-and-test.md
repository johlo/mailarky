# Run, browse, and test Mailarky

## Start locally

```sh
docker compose up -d --build --wait
python3 scripts/smoke.py
```

Compose publishes SMTP 1025, TLS IMAP 1993, and HTTP 8026 to loopback. It enables
SMTP STARTTLS using the bundled test certificate. Override published ports:

```sh
MAILARKY_SMTP_PORT=11025 MAILARKY_IMAP_PORT=11993 MAILARKY_HTTP_PORT=18026 \
  docker compose up -d --build --wait
MAILARKY_SMTP_PORT=11025 MAILARKY_IMAP_PORT=11993 \
  MAILARKY_HTTP_URL=http://localhost:18026 python3 scripts/smoke.py
```

Compose host-port settings do not change container listener ports. When
running the binary directly, the same port variables change listener ports.
Full bind-address variables override them; see [configuration](../reference/configuration.md).

## View mail in a browser

```sh
docker compose --profile webmail up -d --build --wait
```

Open <http://localhost:8027>. Use `user@example.test` / `local-imap-only` or the
credentials from account creation. Roundcube uses TLS IMAP and SMTP STARTTLS,
trusts the bundled certificate, and authenticates both protocols with the
logged-in account. Outgoing mail appears in that account's `Sent` folder.
`MAILARKY_WEBMAIL_PORT` changes the browser port.

Roundcube is optional and talks to the real mail protocols. Mailarky has no
built-in mailbox browser. Its HTTP inspection endpoints are intended for tests.

## Persist state

Add a Compose override with a writable volume and database path:

```yaml
services:
  mailarky:
    environment:
      MAILARKY_DATABASE: /data/mail.db
    volumes:
      - ./mail-data:/data
```

The host directory must be writable by container UID 65532. The default account
uses `/data/mail.db`; additional accounts use files under `/data/mail.db.mailboxes`.
Preserve both. Messages, flags, credentials, and UID identity survive restart;
fault rules do not. Retention is unlimited unless explicitly configured.

## Build and run tests

```sh
make test
go build -o mailarky ./cmd/mailarky
make run
```

Go tests exercise the API and real SMTP/IMAP sockets, including concurrent
account isolation, fault recovery, read flags, sequence numbers, IDLE,
persistence, retention, and notifications. Python's standard-library clients
provide an independent Docker smoke test. Python is not a runtime dependency.

The public protocol forks have their own module paths and CI. To update one,
run `go get github.com/johlo/go-imap/v2@COMMIT` or
`go get github.com/johlo/go-smtp@COMMIT`, then `go mod tidy`, `make test`, and
the Docker smoke test. Run `make diagrams` after editing diagram sources.

## Stop

```sh
docker compose --profile webmail down
```

An in-memory store is discarded when the process stops. The smoke test creates
and removes its own accounts without deleting other accounts' messages.
