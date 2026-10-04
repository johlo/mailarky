# Connect an application

Start the [Compose stack](run-and-test.md), create an account, and configure
both mail clients with the credentials returned by `POST /api/v1/accounts`.

| Setting | Application on host | Application in the same Compose network |
| --- | --- | --- |
| SMTP host | `localhost` | `mailarky` |
| SMTP port | `1025` or `MAILARKY_SMTP_PORT` | `1025` |
| SMTP security | STARTTLS with the bundled CA trusted | STARTTLS with the bundled CA trusted |
| IMAP host | `localhost` | `mailarky` |
| IMAP port | `1993` or `MAILARKY_IMAP_PORT` | `1993` |
| IMAP security | Implicit TLS | Implicit TLS |
| HTTP base | `http://localhost:8026/api/v1` | `http://mailarky:8026/api/v1` |

Trust `testdata/tls/server.crt`; it covers `localhost`, `mailarky`, and
`127.0.0.1`. The private key is an intentional public test fixture. Supply your
own certificate when using a different hostname.

Authenticate SMTP even if authentication is optional: AUTH is what ties
outgoing customer mail to the test account. The recipients can be arbitrary
addresses, including the same addresses used by other tests. Mail is stored in
that account's `Sent`; no external delivery occurs. Seed incoming history in
`INBOX` or a chosen folder via HTTP.

For an application that cannot use SMTP AUTH, configure its recipients to the
account's registered recipient addresses. Unclaimed anonymous recipients route
to `default`.

When running the binary outside Compose, enable STARTTLS with
`MAILARKY_SMTP_TLS_MODE=starttls`, or explicitly allow local plaintext AUTH
with `MAILARKY_SMTP_AUTH_ALLOW_INSECURE=true`. IMAP always uses implicit TLS.
