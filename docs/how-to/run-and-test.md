# Run and test the emulator

Run the commands below from the emulator checkout unless stated otherwise.

## Choose host ports

Set these variables before running Compose:

```sh
export IMAP_EMULATOR_PORT=11993
export IMAP_EMULATOR_HTTP_PORT=18026
docker compose up -d --build --wait --wait-timeout 60
curl -fsS http://localhost:18026/healthz
```

Host clients now use IMAP port 11993 and HTTP port 18026. Inside the container,
the server still listens on 1993 and 8026. Keep the same environment when
running subsequent Compose commands, and update your test client's URLs.

For separate instances, also set distinct `COMPOSE_PROJECT_NAME` values and
choose distinct host ports. See [configuration](../reference/configuration.md)
for the distinction between Compose and process variables.

## Run from Go source

Install the Go version in [go.mod](../../go.mod), then run:

```sh
make test
make run
```

`make test` runs the Go tests with the race detector and runs `go vet`.
`make run` loads the bundled certificate and starts the process in the
foreground. Stop it with Ctrl+C. It uses ports 1993 and 8026 by default; to
avoid another running instance:

```sh
IMAP_EMULATOR_PORT=11993 IMAP_EMULATOR_HTTP_PORT=18026 make run
```

Unlike Compose's published-port overrides, these values change the process's
actual listener ports. Direct runs bind both listeners on all interfaces.

## Use a custom certificate

Provide a PEM certificate and matching PEM private key. The certificate must
cover the hostname your client uses; configure the client to trust its issuer.
For a direct run, set the paths explicitly:

```sh
IMAP_EMULATOR_CERT=/absolute/path/server.crt \
IMAP_EMULATOR_KEY=/absolute/path/server.key \
go run .
```

Use `go run .` for this command: `make run` explicitly selects the bundled
fixture paths. For Docker, merge these mounts into the service configuration:

```yaml
services:
  imap-emulator:
    volumes:
      - ./local-tls/server.crt:/certs/server.crt:ro
      - ./local-tls/server.key:/certs/server.key:ro
```

The container runs as UID/GID `65532:65532`; both mounted files and their
parent directories must be accessible to that user. Keep custom private keys
out of Git. To regenerate the shared public test fixtures, use the
[certificate maintenance instructions](../../testdata/tls/README.md) and
update the certificate trusted by each consuming application.

## Run in CI

The repository's [workflow](../../.github/workflows/ci.yml) runs `make test`,
builds and starts the container, waits for health, and seeds a message. To
exercise your own application, use the same lifecycle:

```sh
docker compose up -d --build --wait --wait-timeout 60
```

Then seed fixtures and run the application's integration tests. The
[integration guide](integrate-with-application.md#drive-your-applications-import)
describes what those tests should wait for and assert. A healthy fixture
endpoint alone does not verify your client's authentication or TLS trust.

Collect logs and stop the service in your CI runner's always-run cleanup step,
including when a test fails:

```sh
docker compose logs
docker compose down
```

If CI checks out the emulator as a private submodule, initialize that submodule
using credentials with access to it before building. The Docker image builds
from source and needs no private container registry login.

[Documentation index](../README.md)
