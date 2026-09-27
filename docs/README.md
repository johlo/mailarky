# Documentation

The service combines SMTP capture, TLS IMAP and an HTTP inspection/control API.
These guides separate learning, practical tasks, reference and explanation.

## Tutorial

[Your first shared mailbox](tutorials/first-mailbox.md): send with SMTP, inspect
through HTTP, and read the same message with IMAP.

## How-to guides

- [Connect your application](how-to/integrate-with-application.md)
- [Give each test a separate mailbox](how-to/separate-mailboxes.md)
- [Create isolated failures with toxics](how-to/toxics.md)
- [Seed test scenarios](how-to/seed-test-scenarios.md)
- [Run locally, persist mail and test in CI](how-to/run-and-test.md)
- [Troubleshoot](how-to/troubleshoot.md)

## Reference

- [HTTP API](reference/http-api.md) and [OpenAPI](../openapi.yaml)
- [Configuration](reference/configuration.md)
- [IMAP behavior](reference/imap-behavior.md)
- [Feature compatibility and deliberate differences](reference/compatibility.md)

## Explanation

[Shared storage, protocol boundaries and test isolation](explanation/design.md)
