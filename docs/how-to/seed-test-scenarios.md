# Seed test scenarios

These recipes assume a running emulator and curl. Set the fixture URL once:

```sh
export IMAP_FIXTURE_URL=http://localhost:8026
```

Adjust the port for your environment. Each successful POST returns HTTP 201
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

The fixture has the `\Seen` flag from creation. Changing flags later is
unsupported. Nothing is delivered to the recipients. To represent an email
captured by a separate SMTP service, seed its details and Message-ID explicitly.

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
The emulator retains both copies. Repeating a POST also appends another copy;
it is not an upsert, even when the Message-ID already exists.

## Isolate tests that share a server

Generate a unique address or Message-ID for each test, then search and assert
using that value. Include the unique identifier in all copies belonging to a
single deduplication test. Seed fixtures before triggering the application's
sync, and use bounded polling to wait for results.

Avoid asserting global mailbox counts in parallel tests. There is one account
shared by all clients, and concurrent tests can append additional messages.
Use a separate Compose project and distinct host ports when a test needs a
completely isolated mailbox.

## Reset an isolated mailbox

When no other test uses your instance, run this from its checkout with the
same Compose project selected:

```sh
docker compose restart imap-emulator
docker compose up -d --wait --wait-timeout 60
```

All folders are now empty. Reconnect IMAP clients and let your importer process
the new UIDVALIDITY before seeding the next scenario. There is no HTTP delete
or reset endpoint, and IMAP deletion is unsupported. Restarting a shared
instance also removes other tests' fixtures.

[Documentation index](../README.md)
