# IMAP emulator documentation

The emulator provides a TLS IMAP mailbox and an HTTP API for adding synthetic
messages. It runs as a standalone process or Docker service.

These docs follow [Diátaxis](https://diataxis.fr/), separating learning,
task completion, technical lookup, and understanding.

## Tutorial: learn by doing

- [Your first test mailbox](tutorials/first-mailbox.md): start a server, seed an
  email, read it over verified TLS, and stop the server.

## How-to guides: complete a task

- [Integrate with your application](how-to/integrate-with-application.md):
  connect from the host or another Compose service and configure TLS trust.
- [Seed test scenarios](how-to/seed-test-scenarios.md): create sent and historical
  messages, duplicate Message-IDs, initial flags, and isolated test fixtures.
- [Run and test the emulator](how-to/run-and-test.md): change ports, run from
  source, use custom certificates, and add a service to CI.
- [Troubleshoot](how-to/troubleshoot.md): diagnose startup, TLS, login, and
  fixture failures.

## Reference: look up a fact

- [Configuration](reference/configuration.md): environment variables, defaults,
  fixed credentials, and container settings.
- [HTTP API](reference/http-api.md): endpoints, fields, validation, and errors.
- [IMAP behavior](reference/imap-behavior.md): supported operations, message
  identity, dates, and limitations.
- [OpenAPI specification](../openapi.yaml): machine-readable HTTP contract.

## Explanation: understand the design

- [How the emulator works](explanation/design.md): the HTTP-to-IMAP flow,
  in-memory storage, synchronization, and the boundary with SMTP.

New here? Start with the [tutorial](tutorials/first-mailbox.md).
Return to the [project README](../README.md).
