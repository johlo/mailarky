# Run, browse, and test Mailarky

## Start locally

For a published image, use the [quick start](../../README.md#quick-start).
To build from source:

```sh
git clone https://github.com/johlo/mailarky.git
cd mailarky
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

## Run without Docker

Download the archive for your operating system and architecture from
[GitHub Releases](https://github.com/johlo/mailarky/releases). Linux and macOS
use `.tar.gz`; Windows uses `.zip`. macOS archives are named `darwin`.
Verify the archive against `checksums.txt`, then extract it. Each archive
contains the executable, documentation, and the public test certificate/key
in `testdata/tls`. For example, from the extracted directory on Linux or macOS:

```sh
./mailarky \
  --imap-tls-cert=testdata/tls/server.crt \
  --imap-tls-key=testdata/tls/server.key \
  --smtp-tls-mode=starttls \
  --smtp=127.0.0.1:1025 \
  --imap=127.0.0.1:1993 \
  --http=127.0.0.1:8026
```

Trust `testdata/tls/server.crt` in your test clients. On Windows, use
`mailarky.exe` and put the arguments on one line. Alternatively, install with
Go and generate your own test certificate as follows.

Install with Go, using the version required by [`go.mod`](../../go.mod) or newer:

```sh
go install github.com/johlo/mailarky/cmd/mailarky@latest
mailarky --help
mailarky --version
```

Ensure Go's install directory (`GOBIN`, or `$(go env GOPATH)/bin` by default)
is on your `PATH`. Replace `@latest` with a release tag or commit to pin a build.

IMAP requires TLS. The Docker image supplies certificates at `/certs`; a
standalone binary needs certificate files of its own. With OpenSSL 1.1.1 or
newer, generate a local test certificate in your working directory:

```sh
mkdir -p mailarky-certs
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 365 \
  -keyout mailarky-certs/server.key -out mailarky-certs/server.crt \
  -subj '/CN=Mailarky local test server' \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'extendedKeyUsage=serverAuth'

mailarky \
  --imap-tls-cert=mailarky-certs/server.crt \
  --imap-tls-key=mailarky-certs/server.key \
  --smtp-tls-mode=starttls \
  --smtp=127.0.0.1:1025 \
  --imap=127.0.0.1:1993 \
  --http=127.0.0.1:8026
```

Trust `mailarky-certs/server.crt` in your SMTP and IMAP clients. SMTP STARTTLS
uses the same certificate. The default account is `user@example.test` with
password `local-imap-only`; tests can create isolated accounts through HTTP.
In another terminal, check `curl -fsS http://127.0.0.1:8026/healthz`.
Stop the server with Ctrl-C.

You can also supply an existing certificate and key with `MAILARKY_IMAP_CERT`
and `MAILARKY_IMAP_KEY`. From a source checkout, `make run` uses the bundled
test certificate. See [TLS configuration](../reference/configuration.md#tls)
for separate SMTP certificates and other options.

## View mail in a browser

From a source checkout, start the optional Roundcube profile:

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

`mailarky --version` reports the version embedded by Go, including the module
version for `go install ...@VERSION`. Local builds include the Git revision
and a `dirty` marker when that metadata is available; builds without version
metadata report `devel`. To set a release version explicitly, build with:

```sh
go build -trimpath \
  -ldflags '-X github.com/johlo/mailarky/internal/sandbox.version=v0.1.0' \
  -o mailarky ./cmd/mailarky
```

Use the version being released in place of `v0.1.0`. Builds without Git
metadata, such as Docker builds, can also set
`-X github.com/johlo/mailarky/internal/sandbox.revision=COMMIT`.

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
