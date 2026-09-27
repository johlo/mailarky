# Run and test

## Docker

```sh
docker compose up -d --build --wait     # start
docker compose logs mail-sandbox        # logs
docker compose down                     # stop (in-memory mail is lost)
```

To run a second copy alongside, use another project name and host ports:

```sh
MAIL_SANDBOX_SMTP_PORT=11025 MAIL_SANDBOX_IMAP_PORT=11993 MAIL_SANDBOX_HTTP_PORT=18026 \
  docker compose -p another-mailbox up -d --build --wait
```

These variables change only the published host ports. Pass the same `-p` and
variables to later Compose commands for that copy.

Health checks: `/healthz`, `/livez`, `/readyz`. The image runs as UID/GID 65532.

## Persist mail

By default everything is in memory. To keep mail across restarts, create a
`compose.override.yaml` next to `compose.yaml`:

```yaml
services:
  mail-sandbox:
    environment:
      MP_DATABASE: /data/mail.db
      MP_MAX_MESSAGES: "0"
    volumes:
      - ./mail-data:/data
```

The `mail-data` directory must be writable by UID 65532. Then run
`docker compose up -d --wait`.

- Messages, flags, tags, folders, UIDs and UIDVALIDITY survive restarts.
  Toxics and pending webhooks do not.
- Only one process may use the database at a time. Stop the service before
  backing up the directory.
- Retention: `MP_MAX_MESSAGES` (default 500, `0` = unlimited) and `MP_MAX_AGE`
  (e.g. `24h`, measured from arrival). Both apply per account.

## Develop

Requires the Go version in `go.mod`.

```sh
make test    # go test -race ./... and go vet
make run     # run locally with the bundled TLS certificate
```

## CI

The CI workflow runs the Go tests, then a smoke test against the container:

```sh
make test
docker compose up -d --build --wait
python3 scripts/smoke.py
docker compose down
```

## Sendmail mode

The binary can also submit an `.eml` file to the SMTP listener:

```sh
go build -o mail-sandbox ./cmd/mail-sandbox
./mail-sandbox sendmail -S localhost:1025 -t < fixture.eml
./mail-sandbox sendmail -S localhost:1025 -f sender@example.test recipient@example.test < fixture.eml
```

`-t` reads recipients from To/Cc/Bcc and strips Bcc. `MP_SENDMAIL_SMTP_ADDR`
sets the default server. It does not authenticate.

## Upgrading from imap-emulator

Stop the old stack, change the hostname to `mail-sandbox`, and trust the new
`testdata/tls/server.crt`. Old environment names still work
([aliases](../reference/configuration.md#previous-environment-names)).
