# Integrate with your application

Use this guide when you already have an IMAP client or importer to test.
For a complete first run, use the [tutorial](../tutorials/first-mailbox.md).

## Connect from the host

Start the emulator with `docker compose up -d --build --wait --wait-timeout 60`
from its checkout. Configure your application with:

| Setting | Value |
| --- | --- |
| Host | `localhost` |
| Port | `1993`, or your chosen host port |
| Transport | Implicit TLS, enabled immediately on connection |
| Username | `clinic@example.test` |
| Password | `local-imap-only` |
| Folders | `INBOX`, `Sent`, `Archive` |
| Trusted certificate | `testdata/tls/server.crt` from this checkout |

Load the certificate into your client's trusted root pool. Keep certificate
and hostname verification enabled. The bundled certificate covers `localhost`,
`127.0.0.1`, and `imap-emulator`. This is an implicit-TLS endpoint, so select
your client's SSL/TLS mode rather than a STARTTLS upgrade mode.

## Connect from another Compose service

Place an emulator checkout at `./imap-emulator` relative to your application's
Compose file. For a Git-managed application, you can pin that dependency:

```sh
git submodule add https://github.com/johlo/imap-emulator.git imap-emulator
```

After cloning an application that already records the submodule, initialize it
with `git submodule update --init -- imap-emulator` instead. Each developer or
CI runner needs permission to read the private repository.

Merge this fragment into the application's Compose file, under its existing
`app` service (substitute your service name):

```yaml
services:
  app:
    depends_on:
      imap-emulator:
        condition: service_healthy
    volumes:
      - ./imap-emulator/testdata/tls/server.crt:/etc/ssl/certs/imap-emulator.pem:ro
  imap-emulator:
    build: ./imap-emulator
```

For a Linux Go application using the system certificate pool, that mount makes
the certificate available as a trusted root. Other runtimes may require an
explicit CA-file or trust-store setting; point it at the mounted certificate.

Configure the application to use `imap-emulator:1993` and implicit TLS. A test
runner on that Compose network can seed `http://imap-emulator:8026/messages`.
No host port publication is needed for those container-to-container connections.

For tests running on the host, add this to the `imap-emulator` service:

```yaml
    ports:
      - "127.0.0.1:8026:8026"
```

Then seed through `http://localhost:8026/messages`. Publish port 1993 similarly
only if a host process also needs IMAP access.

## Drive your application's import

1. Start the emulator and wait for it to become healthy.
2. Seed messages addressed to identities that your application recognizes.
3. Trigger your application's sync, or wait for its polling job.
4. Poll the application's API or UI for the imported result with a bounded
   timeout that allows for its sync interval.
5. Assert the imported data, including the fixture's Message-ID or unique
   address, so unrelated messages cannot satisfy the test.

The emulator has no application database, patient registry, or sync scheduler.
`POST /messages` confirms an append to the mailbox; it does not wait for your
application to import the message.

See [seed test scenarios](seed-test-scenarios.md) for fixture examples and
[troubleshoot](troubleshoot.md) for connection failures.

[Documentation index](../README.md)
