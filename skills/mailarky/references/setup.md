# Running Mailarky in Compose, CI or without Docker

## Docker Compose

Add a service next to the application:

```yaml
services:
  mailarky:
    image: ghcr.io/johlo/mailarky:v0.2.0
    environment:
      MAILARKY_SMTP_TLS_MODE: starttls
    ports:
      - "127.0.0.1:1025:1025"
      - "127.0.0.1:1993:1993"
      - "127.0.0.1:8026:8026"
```

The image has a health check, so `docker compose up -d --wait` returns once
Mailarky is ready, and other services can use
`depends_on: {mailarky: {condition: service_healthy}}`. Inside the Compose
network, the application connects to host `mailarky` on ports 1025, 1993 and
8026. Copy the certificate with `docker compose cp mailarky:/certs/server.crt
./mailarky.crt`, or download it as described below.

## GitHub Actions

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      mailarky:
        image: ghcr.io/johlo/mailarky:v0.2.0
        env:
          MAILARKY_SMTP_TLS_MODE: starttls
        ports:
          - 1025:1025
          - 1993:1993
          - 8026:8026
    steps:
      - uses: actions/checkout@v5
      - name: Fetch the Mailarky test certificate
        run: curl -fsSL -o mailarky.crt https://github.com/johlo/mailarky/releases/download/v0.2.0/server.crt
      # ... run the tests against localhost:1025, localhost:1993 and http://localhost:8026
```

Every release publishes the same test certificate as `server.crt`.

## Without Docker

Download a binary archive from <https://github.com/johlo/mailarky/releases> or
run `go install github.com/johlo/mailarky/cmd/mailarky@v0.2.0`. Archives
contain the test certificate in `testdata/tls`. IMAP needs a certificate and
key:

```sh
./mailarky \
  --imap-tls-cert=testdata/tls/server.crt --imap-tls-key=testdata/tls/server.key \
  --smtp-tls-mode=starttls \
  --smtp=127.0.0.1:1025 --imap=127.0.0.1:1993 --http=127.0.0.1:8026
```

Bind to `127.0.0.1` as shown: the binary listens on all interfaces by default,
and its API has no authentication.

## Ports already in use

Map different host ports, for example `-p 127.0.0.1:11025:1025`, and point both
the application and the tests at them. `MAILARKY_SMTP_PORT`,
`MAILARKY_IMAP_PORT` and `MAILARKY_HTTP_PORT` change the listening ports when
running the binary directly.

## Configuration

Settings come from environment variables (`MAILARKY_*`), flags or a YAML file
in `MAILARKY_CONFIG`. Common ones:

| Setting | Purpose |
| --- | --- |
| `MAILARKY_SMTP_TLS_MODE` | `starttls` or `tls`; empty means plaintext SMTP |
| `MAILARKY_SMTP_AUTH_ALLOW_INSECURE=true` | Allow SMTP AUTH without TLS, for clients that can't use STARTTLS |
| `MAILARKY_SMTP_REQUIRE_AUTH=true` | Reject unauthenticated SMTP |
| `MAILARKY_DATABASE=/data/mail.db` | Keep mail across restarts (needs a writable volume) |
| `MAILARKY_HTTP_AUTH=user:password` | Protect the HTTP API |

Full reference: <https://github.com/johlo/mailarky/blob/main/docs/reference/configuration.md>
