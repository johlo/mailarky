# Seed test scenarios

These recipes assume a running emulator and curl. Set the fixture URL once:

```sh
export IMAP_FIXTURE_URL=http://localhost:8026
```

Adjust the port for your environment. For a provisioned account, append its
returned `api_base`, for example
`export IMAP_FIXTURE_URL=http://localhost:8026/mailboxes/GENERATED-UUID`.
Each successful fixture POST returns HTTP 201
with `folder` and `message_id`. The [HTTP reference](../reference/http-api.md)
describes every field.

## Create historical mail with a later received date

Set `date` to the message's original time and `internal_date` to the time it
entered the mailbox:

```sh
curl -fsS "${IMAP_FIXTURE_URL}/messages" \
  -H 'Content-Type: application/json' \
  -d '{"from":"alice@example.test","to":["clinic@example.test"],"subject":"Historical reply","message_id":"history@example.test","date":"2020-01-02T03:04:05Z","internal_date":"2021-02-03T04:05:06Z"}'
```

Assert that your application uses the intended timestamp. Neither field is
your application's ingestion time; that time is determined when it imports
the message. To create mail dated now, omit both fields.

## Create a sent message with initial flags

Choose `Sent`, make the clinic the sender, and supply the recipients:

```sh
curl -fsS "${IMAP_FIXTURE_URL}/messages" \
  -H 'Content-Type: application/json' \
  -d '{"folder":"Sent","from":"Clinic <clinic@example.test>","to":["alice@example.test"],"cc":["copy@example.test"],"subject":"Your appointment","message_id":"sent@example.test","flags":["\\Seen"],"body":"A synthetic appointment message."}'
```

The fixture has the `\Seen` flag from creation. HTTP read updates and IMAP STORE
can change it later. Fixtures do not deliver externally. App SMTP deliveries are
already captured in the same Sent folder, so no synthetic copy is needed.

## Test deduplication across folders

Append the same Message-ID to two folders:

```sh
for folder in INBOX Archive; do
  curl -fsS "${IMAP_FIXTURE_URL}/messages" \
    -H 'Content-Type: application/json' \
    -d "{\"folder\":\"${folder}\",\"from\":\"alice@example.test\",\"to\":[\"clinic@example.test\"],\"subject\":\"Copied message\",\"message_id\":\"duplicate@example.test\"}"
done
```

Run your importer against both folders and check its deduplication behavior.
With the default `MP_IGNORE_DUPLICATE_IDS=false`, the emulator retains both
copies. Repeating a POST also appends another copy; it is not an upsert, even
when the Message-ID already exists. Leave duplicate suppression disabled when
testing your application's deduplication. When enabled, the emulator suppresses
repeated Message-IDs across folders within the same account.

## Isolate tests that share a server

Generate a unique address or Message-ID for each test, then search and assert
using that value. Include the unique identifier in all copies belonging to a
single deduplication test. Seed fixtures before triggering the application's
sync, and use bounded polling to wait for results.

Avoid asserting global mailbox counts in parallel tests. Concurrent tests in the default account can append additional messages.
[Provision an account per test](separate-mailboxes.md) for independent state.
Use a separate Compose project and distinct host ports when a test needs
different service-wide settings, such as TLS or outbound relay configuration.

## Import custom MIME

```sh
curl -fsS "${IMAP_FIXTURE_URL}/api/v1/messages/raw?folder=INBOX" \
  -H 'Content-Type: message/rfc822' --data-binary @fixture.eml
```

This preserves raw MIME, including X-Test-ID headers used by
[scoped toxics](toxics.md). JSON /api/v1/send can also construct multipart messages
with base64 attachments. Fixture /messages remains convenient for historical mail.

## Delete owned fixtures

Find IDs using your unique Message-ID or address, then delete only those IDs:

```sh
curl -fsS -X DELETE "${IMAP_FIXTURE_URL}/api/v1/messages" \
  -H 'Content-Type: application/json' -d '{"IDs":["YOUR-MESSAGE-UUID"]}'
```

Never send an empty ID array in shared tests: it means all mail. For a completely
isolated in-memory instance, restarting also clears mail and regenerates
UIDVALIDITY. Persistent storage survives restarts. Toxics always reset on restart.

[Documentation index](../README.md)
