# Connect an application

| Setting | Value |
| --- | --- |
| SMTP | `mail-sandbox:1025` in Compose, `localhost:1025` from the host. No authentication, no TLS. |
| IMAP | `mail-sandbox:1993` / `localhost:1993`, implicit TLS |
| IMAP login | `clinic@example.test` / `local-imap-only` |
| IMAP trust | Add [`testdata/tls/server.crt`](../../testdata/tls/server.crt) to the client's trusted roots |
| HTTP API | `http://mail-sandbox:8026` / `http://localhost:8026` |

SMTP deliveries are stored in `Sent`. If your application expects them in
`INBOX`, set `MAIL_SANDBOX_SMTP_FOLDER=INBOX` on the sandbox.

In the IMAP client, use UID SEARCH and UID FETCH and track UIDVALIDITY. The
server does not push updates, so poll. See [IMAP behavior](../reference/imap-behavior.md).

## Parallel tests

Give each test its own mailbox ([separate mailboxes](separate-mailboxes.md)).
If tests share the default account instead:

- Use a unique address or `X-Test-ID` header per test, and search by it.
- Set `MP_MAX_MESSAGES=0`, or retention can evict another test's messages.
- Delete only the message IDs the test created.

## Pinning

Pin this repository as a Git submodule, or pin a built image. To run several
stacks side by side, change the published host ports and keep the container
ports as they are ([run and test](run-and-test.md)).

The sandbox never delivers mail onward, so a test stack needs no real mail
credentials and can't email real people.
