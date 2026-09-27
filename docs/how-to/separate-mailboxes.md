# Give each test a separate mailbox

Provision accounts through the management API. Each account has independent
messages, folders, UID counters, read flags, tags, retention, toxics and
notifications. Accounts share the server's SMTP/IMAP/HTTP listener ports.

## Create an account

```sh
curl -fsS http://localhost:8026/api/v1/mailboxes \
  -H 'Content-Type: application/json' \
  -d '{"name":"test-unique-id","recipients":["test-unique-id@example.test"]}'
```

The response includes `id`, `name`, `username`, `password`, `recipients`, `created`
and `api_base`, for example `/mailboxes/GENERATED-UUID`. Save the credentials and
API base in the test fixture. The password is returned only at creation; listings
never expose passwords or hashes. You can supply username/password explicitly.
Omitting recipients generates a unique `UUID@mailbox.test` address. An empty
creation body `{}` generates all values, convenient for parallel tests.

Names, usernames and active recipient bindings must be unique. Recipient matching
is exact and case-insensitive; wildcards are rejected. Settings/bindings are
immutable for an account. Delete and recreate it to change them.

## Send and inspect mail

Send SMTP to the same server on port 1025, with an envelope recipient from the
account's `recipients` list. It is stored in that account's configured SMTP
folder (`Sent` by default).
Unassigned addresses continue going to the existing `default` account. SMTP
routing uses the envelope, not the To header or the SMTP authentication username.
SMTP authentication remains a server-wide setting; the generated account
credentials are for IMAP.

For HTTP, prefix the ordinary API paths with `api_base`:

```sh
MAILBOX_API=http://localhost:8026/mailboxes/GENERATED-UUID
curl -fsS "$MAILBOX_API/api/v1/messages"
curl -fsS "$MAILBOX_API/messages" -H 'Content-Type: application/json' \
  -d '{"from":"alice@example.test","to":["test-unique-id@example.test"],"subject":"Incoming fixture"}'
```

Explicit HTTP/IMAP appends stay in the selected account regardless of recipient
headers. This lets you seed historical mail. SMTP deliveries route by the
registered envelope bindings. The root API paths continue addressing only the
default account; `/mailboxes/default/...` is its equivalent scoped prefix.

Connect to TLS IMAP on port 1993 with the returned username/password. The login
selects the account. Each starts with INBOX, Sent and Archive, plus the configured
SMTP folder if different. A user cannot list
or fetch another account's folders. UIDs may have identical numbers across
accounts; mailbox identity is part of the cursor key.

## Scope failures and cleanup

Create toxics under `$MAILBOX_API/api/v1/toxics`. All normal lifecycle operations
work inside that account, and names may be reused in other accounts. Message
selectors are still required; this preserves isolation among tests sharing an
account too. See [toxics](toxics.md).

An SMTP transaction may target only one account. RCPT TO for a different account
returns 553; use separate transactions/connections for deliveries to different
accounts. This prevents a DATA rejection for one account from failing delivery
to another account in the same SMTP transaction. The target account becomes known
at the first RCPT TO, so sender-stage toxics run there, before recipient-stage
toxics. They cannot run at MAIL FROM before the account is known.

Clean up the whole test-owned account in `finally`/defer:

```sh
curl -fsS -X DELETE http://localhost:8026/api/v1/mailboxes/GENERATED-UUID
```

Deletion removes its messages, toxics, subscriptions and database file, and
existing IMAP sessions stop reading its data. Other accounts are unaffected.
The default account cannot be deleted. Late SMTP to a deleted recipient gets
550 until that address is explicitly assigned to a new account; it never falls
through into default. An in-flight operation may finish before deletion takes
effect, but new writes to the closed store fail.

## Inspect and persist accounts

`GET /api/v1/mailboxes` lists accounts, including default.
`GET /api/v1/mailboxes/{id}` returns one account's metadata without its password.
With MP_DATABASE configured, the main database stores the account catalog and
bcrypt password hashes; message databases live in `MP_DATABASE.mailboxes/`.
Back up the whole data directory with the service stopped. Empty mailboxes also
retain UIDVALIDITY across restarts. Toxics and pending notification queues remain
ephemeral. Without persistence, runtime accounts disappear on restart.

HTTP access uses the configured service/send API credentials. API scoping isolates
test state; it is not separate HTTP authorization for mutually untrusted tenants.
Message summaries/webhooks include MailboxID, and webhooks additionally send the
Mail-Sandbox-Mailbox header. Scoped WebSocket streams contain only that account's
updates. Retention limits apply independently to each account.
