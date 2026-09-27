# Connect an application

Run the service with `docker compose up -d --build --wait`. Applications on the
Compose network use SMTP `imap-emulator:1025`, TLS IMAP `imap-emulator:1993`, and
HTTP `http://imap-emulator:8026`. Host applications use `localhost` instead.

SMTP accepts unauthenticated test deliveries by default. Configure the sending
application's SMTP host/port accordingly. Captured deliveries appear immediately
in `Sent` and through `/api/v1/messages`. Use `SMTP_EMULATOR_FOLDER=INBOX` inside
the server process if your application expects deliveries in INBOX.

IMAP credentials default to `clinic@example.test` / `local-imap-only`. Install
`testdata/tls/server.crt` in the client's trust store and connect with implicit
TLS. Use UID searches/fetches and track UIDVALIDITY. See [IMAP behavior](../reference/imap-behavior.md).

Mailpit HTTP clients can point to the shared API on port 8026. The common
message/search/attachment/tag/read/delete/send/release endpoints retain their
paths and field casing. [Compatibility](../reference/compatibility.md) lists
intentional differences. No browser UI is served.

Pin the repository as a submodule or pin a built image. For isolated CI stacks,
publish distinct host ports while keeping container ports fixed. For concurrent
tests in one stack, use unique addresses or `X-Test-ID` and
[message-scoped toxics](toxics.md); set `MP_MAX_MESSAGES=0` to prevent global
retention from evicting another test's messages. Delete only owned IDs afterward.

External forwarding/relay is opt-in. Configure it only when the test actually
needs an outbound SMTP peer; an ordinary capture stack needs no external mail
credentials. Refer to [configuration](../reference/configuration.md).
