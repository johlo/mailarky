# Run, persist and test

## Docker and host ports

```sh
docker compose up -d --build --wait
MAIL_SANDBOX_SMTP_PORT=11025 MAIL_SANDBOX_IMAP_PORT=11993 MAIL_SANDBOX_HTTP_PORT=18026   docker compose -p another-mailbox up -d --build --wait
```

Host port overrides do not change container listeners. Use the same project name
and overrides when running subsequent Compose commands. `docker compose down`
stops the default stack. In-memory messages and all toxics disappear on restart.

When upgrading from `imap-emulator`, stop the old Compose stack before starting
the renamed `mail-sandbox` service. Update application hostnames and trust the
new `testdata/tls/server.crt`, which covers the `mail-sandbox` Docker hostname.
The executable is now `mail-sandbox`; metrics use `mail_sandbox_` and the webhook
mailbox header is `Mail-Sandbox-Mailbox`. Message APIs and stored databases keep
their formats. Existing environment names remain
[compatibility aliases](../reference/configuration.md#previous-environment-names).

## Local Go development

Install the Go version in `go.mod`, then run `make run`. It points IMAP TLS at
the bundled fixture certificate. `make test` runs race-checked tests and vet.
The executable is in `cmd/mail-sandbox`; implementation and package tests live
in `internal/sandbox`. Run `go test ./...` from the repository root to include
all packages. Shared TLS fixtures remain in `testdata/tls`.
All protocol tests use ephemeral local ports and synthetic messages, including
SMTP authentication/TLS, relay, persistence, diagnostics and concurrent toxics.

## Persist mail

Create a host directory writable by container UID 65532. Save this alongside
`compose.yaml` as `compose.override.yaml`, then recreate with
`docker compose up -d --wait`:

```yaml
services:
  mail-sandbox:
    environment:
      MP_DATABASE: /data/mail.db
      MP_MAX_MESSAGES: "0"
    volumes:
      - ./mail-data:/data
```

The database is bbolt, not a Mailpit SQLite file. Message MIME, flags, tags,
folders, UIDs and UIDVALIDITY survive restart. Only one service process may own
the file. Provisioned accounts additionally use the sibling `mail.db.mailboxes/`
directory. Stop the service and back up the entire data directory together;
copying actively written files is not a supported backup procedure. Do not commit test
mail or databases. Toxics and pending webhook notifications remain ephemeral.

Use `MP_MAX_MESSAGES` (500 by default; 0 unlimited) and `MP_MAX_AGE=24h` for
retention. Age is based on ingestion, not the original Date header. Retention
applies independently to each account; disable it for suites sharing an account.

## CI

The workflow runs `make test`, builds/starts Docker, then runs
`scripts/smoke.py` against SMTP, HTTP and verified TLS IMAP. Reproduce it with:

```sh
make test
docker compose up -d --build --wait
python3 scripts/smoke.py
docker compose down
```

Inspect logs with `docker compose logs mail-sandbox`. The image runs as UID/GID
65532 and includes public TLS fixtures and the CA trust bundle for optional
outbound HTTPS/SMTP. Health probes are `/healthz`, `/livez`, and `/readyz`.

## Sendmail-compatible submission

Build the local binary with `go build -o mail-sandbox ./cmd/mail-sandbox`,
then submit MIME:

```sh
./mail-sandbox sendmail -S localhost:1025 -t < fixture.eml
./mail-sandbox sendmail -S localhost:1025 -f sender@example.test recipient@example.test < fixture.eml
```

`-t` extracts To/Cc/Bcc recipients; the Bcc header is removed for delivery.
`-i`/`-oi` are accepted. This client mode targets an unauthenticated test SMTP
listener. `MP_SENDMAIL_SMTP_ADDR` sets its default address.
