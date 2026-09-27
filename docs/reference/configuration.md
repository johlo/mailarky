# Configuration reference

## Fixed defaults

| Property | Value |
| --- | --- |
| IMAP transport | Implicit TLS; minimum TLS 1.2 |
| IMAP port | `1993` |
| Fixture API transport | Plain HTTP |
| Fixture API port | `8026` |
| Username | `clinic@example.test` |
| Password | `local-imap-only` |
| Initial folders | `INBOX`, `Sent`, `Archive`; initially empty and subscribed |
| HTTP authentication | None |
| Persistence | None; messages remain only for the process lifetime |

Credentials, folders, and HTTP authentication are not configurable. There are
no command-line flags or configuration files for the server.

## Process environment

These variables are read by the binary. Empty values use the defaults.

| Variable | Default | Meaning |
| --- | --- | --- |
| `IMAP_EMULATOR_PORT` | `1993` | TLS IMAP listener port |
| `IMAP_EMULATOR_HTTP_PORT` | `8026` | HTTP fixture listener port |
| `IMAP_EMULATOR_CERT` | `/certs/server.crt` | PEM server certificate path |
| `IMAP_EMULATOR_KEY` | `/certs/server.key` | PEM private key path |

Both listeners bind on all interfaces. Certificate loading or listener startup
failure exits the process. Interrupt and SIGTERM signals close the servers.

`make run` explicitly sets the certificate paths to `testdata/tls/server.crt`
and `testdata/tls/server.key`; other process settings use the environment.

## Compose environment

The supplied [compose.yaml](../../compose.yaml) uses two host-shell variables
for port publication. It does **not** pass them into the container environment.

| Variable | Default host binding | Container destination |
| --- | --- | --- |
| `IMAP_EMULATOR_PORT` | `127.0.0.1:1993` | `1993` |
| `IMAP_EMULATOR_HTTP_PORT` | `127.0.0.1:8026` | `8026` |

Changing either variable for Compose changes the host port only. Containers
on the same network still connect to `imap-emulator:1993` and
`http://imap-emulator:8026`.

## Container and TLS fixtures

| Property | Value |
| --- | --- |
| Local Compose image tag | `imap-emulator:local` |
| Build context | Repository root |
| Process UID/GID | `65532:65532` |
| Bundled certificate and key | `/certs/server.crt`, `/certs/server.key` |
| Certificate names | `imap-emulator`, `localhost`, `127.0.0.1` |
| Health check | HTTP `GET /healthz`, every 2 seconds, 3-second timeout, 15 retries |

The bundled certificate and key are public test fixtures. Source copies live
under [testdata/tls](../../testdata/tls/README.md). A health check reports HTTP
service availability; it does not perform an IMAP login or verify a particular
client's trust configuration.

See [run and test](../how-to/run-and-test.md) for operational commands and
[IMAP behavior](imap-behavior.md) for protocol limits.

[Documentation index](../README.md)
